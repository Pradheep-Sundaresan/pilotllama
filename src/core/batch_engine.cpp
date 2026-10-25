#include "inferno/batch_engine.hpp"

#include "inferno/bounded_queue.hpp"

#include <algorithm>
#include <atomic>
#include <exception>
#include <stdexcept>
#include <utility>

namespace inferno {
namespace {
struct DeliveredPiece {
    std::string bytes;
    OutputChannel channel = OutputChannel::content;
    bool advances_output_tokens = true;
    std::uint32_t tool_call_index = 0;
    std::string tool_call_id;
    std::string tool_name;
};

void advance(RequestLifecycle& lifecycle, RequestState next) {
    if (!lifecycle.transition_to(next)) throw std::logic_error("Invalid batch-engine lifecycle transition");
}
}

struct ContinuousBatchEngine::WorkItem {
    GenerationRequest request;
    std::shared_ptr<RequestLifecycle> lifecycle;
    std::promise<GenerationResult> completion;
    RequestTimings timings;
    InferenceBackend::TokenCallback token;
    std::shared_ptr<RequestControl> control;
};

struct ContinuousBatchEngine::ActiveSlot {
    SequenceId sequence_id = 0;
    std::unique_ptr<WorkItem> item;
    GenerationResult result;
    std::unique_ptr<BoundedQueue<DeliveredPiece>> delivery;
    std::thread delivery_thread;
    std::atomic<bool> delivery_done{true};
    std::mutex delivery_error_mutex;
    std::string delivery_error;
};

struct ContinuousBatchEngine::RetiringSlot {
    std::unique_ptr<ActiveSlot> slot;
    RequestState state = RequestState::failed;
    std::string error;
};

ContinuousBatchEngine::ContinuousBatchEngine(std::unique_ptr<BatchExecutor> executor, BatchEngineConfig config)
    : executor_(std::move(executor)), config_(config) {
    if (!executor_) throw std::invalid_argument("Batch engine requires an executor");
    if (config_.active_sequences != 2 || config_.waiting_capacity != 2 ||
        config_.per_sequence_context_tokens == 0 || config_.max_output_tokens == 0 ||
        config_.max_output_tokens > config_.per_sequence_context_tokens || config_.prefill_chunk_tokens == 0 ||
        config_.delivery_capacity == 0) {
        throw std::invalid_argument("Invalid conservative batch-engine configuration");
    }
    worker_ = std::thread(&ContinuousBatchEngine::work, this);
}

ContinuousBatchEngine::~ContinuousBatchEngine() { shutdown(); }

Submission ContinuousBatchEngine::submit(GenerationRequest request, InferenceBackend::TokenCallback token) {
    auto item = std::make_unique<WorkItem>();
    item->request = std::move(request);
    item->lifecycle = std::make_shared<RequestLifecycle>();
    item->timings.submitted = RequestClock::now();
    item->token = std::move(token);
    item->control = std::make_shared<RequestControl>(item->request.deadline);
    Submission submission{SubmitStatus::invalid, item->lifecycle, item->completion.get_future(), item->control};
    std::string error;
    RequestState immediate = RequestState::rejected;
    {
        std::lock_guard lock(mutex_);
        if (item->request.id == 0 || item->request.max_output_tokens == 0 ||
            item->request.max_output_tokens > config_.max_output_tokens) {
            error = "Request exceeds batch-engine identity or output bounds";
        } else if (item->request.deadline && RequestClock::now() >= *item->request.deadline) {
            submission.status = SubmitStatus::expired;
            immediate = RequestState::timed_out;
            error = "Request deadline exceeded before queue admission";
        } else if (closed_ || failed_) {
            submission.status = SubmitStatus::closed;
            error = failed_ ? failure_ : "Batch engine is closed";
        } else if (waiting_.size() >= config_.waiting_capacity) {
            submission.status = SubmitStatus::full;
            error = "Request queue is full";
        } else {
            item->timings.queued = RequestClock::now();
            advance(*item->lifecycle, RequestState::queued);
            waiting_.push_back(std::move(item));
            submission.status = SubmitStatus::accepted;
            changed_.notify_one();
            return submission;
        }
    }
    (void)item->control->finish();
    advance(*item->lifecycle, immediate);
    item->timings.finished = RequestClock::now();
    item->completion.set_value({item->request.id, immediate, {}, std::move(error), summarize_timings(item->timings)});
    return submission;
}

SchedulerSnapshot ContinuousBatchEngine::scheduler_snapshot() const {
    std::lock_guard lock(mutex_);
    return {waiting_.size(), executing_requests_, config_.waiting_capacity};
}

std::optional<MemorySnapshot> ContinuousBatchEngine::memory_snapshot() const {
    return executor_->memory_snapshot();
}

std::optional<ModelCapabilities> ContinuousBatchEngine::model_capabilities() const {
    return executor_->model_capabilities();
}

void ContinuousBatchEngine::shutdown() {
    std::lock_guard join_lock(shutdown_mutex_);
    {
        std::lock_guard lock(mutex_);
        closed_ = true;
        changed_.notify_all();
    }
    if (worker_.joinable()) worker_.join();
}

void ContinuousBatchEngine::notify_delivery() {
    std::lock_guard lock(mutex_);
    changed_.notify_one();
}

bool ContinuousBatchEngine::admit_one(std::unique_ptr<WorkItem> item) {
    const auto sequence = [&] {
        for (SequenceId candidate = 0; candidate < config_.active_sequences; ++candidate) {
            if (std::none_of(active_.begin(), active_.end(), [candidate](const auto& slot) { return slot->sequence_id == candidate; })) return candidate;
        }
        throw std::logic_error("No free batch sequence");
    }();
    auto slot = std::make_unique<ActiveSlot>();
    slot->sequence_id = sequence;
    slot->result.id = item->request.id;
    slot->item = std::move(item);
    try {
        slot->item->control->checkpoint();
        advance(*slot->item->lifecycle, RequestState::admitted);
        slot->item->timings.admitted = RequestClock::now();
        slot->item->timings.backend_started = RequestClock::now();
        const auto prompt_tokens = executor_->admit(sequence, slot->item->request);
        if (prompt_tokens > config_.per_sequence_context_tokens ||
            slot->item->request.max_output_tokens > config_.per_sequence_context_tokens - prompt_tokens) {
            throw std::length_error("Prompt plus output exceeds the per-sequence context bound");
        }
        advance(*slot->item->lifecycle, RequestState::prefill);
        if (slot->item->token) {
            slot->delivery = std::make_unique<BoundedQueue<DeliveredPiece>>(config_.delivery_capacity);
            slot->delivery_done.store(false);
            auto* raw = slot.get();
            slot->delivery_thread = std::thread([this, raw] {
                try {
                    while (auto piece = raw->delivery->wait_pop()) {
                        raw->item->token({piece->bytes, piece->channel, piece->advances_output_tokens,
                            piece->tool_call_index, piece->tool_call_id, piece->tool_name});
                    }
                } catch (const std::exception& error) {
                    std::lock_guard error_lock(raw->delivery_error_mutex);
                    raw->delivery_error = error.what();
                } catch (...) {
                    std::lock_guard error_lock(raw->delivery_error_mutex);
                    raw->delivery_error = "Unknown token callback exception";
                }
                raw->delivery_done.store(true);
                notify_delivery();
            });
        }
        active_.push_back(std::move(slot));
        {
            std::lock_guard lock(mutex_);
            executing_requests_ = active_.size();
        }
        return true;
    } catch (const RequestStopped& stopped) {
        if (!executor_->remove(sequence)) fail_closed("Sequence cleanup failed after stopped admission");
        slot->item->timings.backend_finished = RequestClock::now();
        (void)slot->item->control->finish();
        advance(*slot->item->lifecycle, stopped.state());
        slot->item->timings.finished = RequestClock::now();
        slot->item->completion.set_value({slot->item->request.id, stopped.state(), {}, stopped.what(), summarize_timings(slot->item->timings)});
    } catch (const std::exception& error) {
        if (!executor_->remove(sequence)) fail_closed("Sequence cleanup failed after admission error");
        slot->item->timings.backend_finished = RequestClock::now();
        (void)slot->item->control->finish();
        advance(*slot->item->lifecycle, RequestState::failed);
        slot->item->timings.finished = RequestClock::now();
        slot->item->completion.set_value({slot->item->request.id, RequestState::failed, {}, error.what(), summarize_timings(slot->item->timings)});
    }
    return false;
}

void ContinuousBatchEngine::retire(std::size_t index, RequestState state, std::string error) {
    auto slot = std::move(active_.at(index));
    active_.erase(active_.begin() + static_cast<std::ptrdiff_t>(index));
    {
        std::lock_guard lock(mutex_);
        executing_requests_ = active_.size();
    }
    if (!executor_->remove(slot->sequence_id)) {
        state = RequestState::failed;
        error = "Per-sequence cleanup failed; batch engine closed";
        fail_closed(error);
    }
    slot->item->timings.backend_finished = RequestClock::now();
    if (slot->delivery) slot->delivery->close();
    retiring_.push_back(std::make_unique<RetiringSlot>(RetiringSlot{std::move(slot), state, std::move(error)}));
}

void ContinuousBatchEngine::reap_deliveries(bool join_all) {
    for (std::size_t i = 0; i < retiring_.size();) {
        auto& retiring = retiring_[i];
        auto& slot = *retiring->slot;
        if (!join_all && !slot.delivery_done.load()) { ++i; continue; }
        if (slot.delivery_thread.joinable()) slot.delivery_thread.join();
        {
            std::lock_guard error_lock(slot.delivery_error_mutex);
            if (!slot.delivery_error.empty()) {
                retiring->state = RequestState::failed;
                retiring->error = slot.delivery_error;
            }
        }
        const auto stop = slot.item->control->finish();
        if (stop && retiring->state == RequestState::completed) {
            retiring->state = *stop;
            retiring->error = *stop == RequestState::cancelled ? "Request cancelled" : "Request deadline exceeded";
        }
        if (retiring->state != RequestState::completed) slot.result.text.clear();
        slot.result.state = retiring->state;
        slot.result.error = std::move(retiring->error);
        advance(*slot.item->lifecycle, slot.result.state);
        slot.item->timings.finished = RequestClock::now();
        slot.result.metrics = summarize_timings(slot.item->timings);
        slot.item->completion.set_value(std::move(slot.result));
        retiring_.erase(retiring_.begin() + static_cast<std::ptrdiff_t>(i));
    }
}

void ContinuousBatchEngine::fail_closed(std::string error) {
    std::lock_guard lock(mutex_);
    failed_ = true;
    closed_ = true;
    failure_ = std::move(error);
    changed_.notify_all();
}

void ContinuousBatchEngine::work() {
    while (true) {
        reap_deliveries(false);
        {
            std::unique_lock lock(mutex_);
            changed_.wait(lock, [&] {
                const bool delivery_ready = std::any_of(retiring_.begin(), retiring_.end(), [](const auto& item) { return item->slot->delivery_done.load(); });
                return closed_ || !waiting_.empty() || !active_.empty() || delivery_ready;
            });
            while (!waiting_.empty() && active_.size() < config_.active_sequences && !failed_) {
                auto item = std::move(waiting_.front());
                waiting_.pop_front();
                lock.unlock();
                admit_one(std::move(item));
                lock.lock();
            }
            if (active_.empty() && waiting_.empty() && closed_) break;
        }
        {
            std::string failure;
            {
                std::lock_guard lock(mutex_);
                if (failed_) failure = failure_;
            }
            if (!failure.empty()) {
                for (std::size_t i = active_.size(); i-- > 0;) retire(i, RequestState::failed, failure);
                continue;
            }
        }
        if (active_.empty()) continue;

        for (std::size_t i = active_.size(); i-- > 0;) {
            try {
                active_[i]->item->control->checkpoint();
                std::lock_guard error_lock(active_[i]->delivery_error_mutex);
                if (!active_[i]->delivery_error.empty()) throw std::runtime_error(active_[i]->delivery_error);
            } catch (const RequestStopped& stopped) {
                retire(i, stopped.state(), stopped.what());
            } catch (const std::exception& error) {
                retire(i, RequestState::failed, error.what());
            }
        }
        if (active_.empty()) continue;

        std::vector<SequenceId> sequences;
        sequences.reserve(active_.size());
        for (const auto& slot : active_) sequences.push_back(slot->sequence_id);
        std::vector<BatchStepEvent> events;
        try {
            events = executor_->step(sequences, config_.prefill_chunk_tokens);
            if (events.size() != sequences.size()) throw std::logic_error("Batch executor returned incomplete routing");
        } catch (const std::exception& error) {
            for (std::size_t i = active_.size(); i-- > 0;) retire(i, RequestState::failed, error.what());
            fail_closed("Batch executor failed; engine closed");
            continue;
        }

        for (std::size_t i = active_.size(); i-- > 0;) {
            auto& slot = *active_[i];
            const auto match = std::find_if(events.begin(), events.end(), [&](const auto& event) { return event.sequence_id == slot.sequence_id; });
            if (match == events.end() || std::count_if(events.begin(), events.end(), [&](const auto& event) { return event.sequence_id == slot.sequence_id; }) != 1) {
                retire(i, RequestState::failed, "Batch executor produced invalid sequence routing");
                fail_closed("Invalid batch routing; engine closed");
                continue;
            }
            try {
                slot.item->control->checkpoint();
                if (match->decode_started && slot.item->lifecycle->state() == RequestState::prefill) {
                    advance(*slot.item->lifecycle, RequestState::decoding);
                    slot.item->timings.decode_started = RequestClock::now();
                }
                if (match->generated_token) {
                    if (slot.item->lifecycle->state() != RequestState::decoding ||
                        slot.item->timings.output_tokens >= slot.item->request.max_output_tokens) {
                        throw std::logic_error("Invalid batch token event or output budget exceeded");
                    }
                    const auto now = RequestClock::now();
                    if (!slot.item->timings.first_token) slot.item->timings.first_token = now;
                    slot.item->timings.last_token = now;
                    ++slot.item->timings.output_tokens;
                } else if (!match->output_pieces.empty() && slot.item->timings.output_tokens == 0) {
                    throw std::logic_error("Batch parser output preceded native token generation");
                }
                bool advances = match->generated_token;
                if (match->output_pieces.empty() && match->generated_token) {
                    if (slot.delivery && slot.delivery->try_push({{}, OutputChannel::content, true}) != PushResult::accepted) {
                        throw std::runtime_error("Token consumer exceeded bounded delivery capacity");
                    }
                }
                for (const auto& piece : match->output_pieces) {
                    if (piece.channel != OutputChannel::tool_call) slot.result.text.append(piece.bytes);
                    if (slot.delivery && slot.delivery->try_push({piece.bytes, piece.channel, advances,
                            piece.tool_call_index, piece.tool_call_id, piece.tool_name}) != PushResult::accepted) {
                        throw std::runtime_error("Token consumer exceeded bounded delivery capacity");
                    }
                    advances = false;
                }
                slot.item->control->checkpoint();
                if (!match->error.empty()) retire(i, RequestState::failed, match->error);
                else if (match->finished) retire(i, RequestState::completed, {});
            } catch (const RequestStopped& stopped) {
                retire(i, stopped.state(), stopped.what());
            } catch (const std::exception& error) {
                retire(i, RequestState::failed, error.what());
            }
        }
        {
            std::string failure;
            {
                std::lock_guard lock(mutex_);
                if (failed_) failure = failure_;
            }
            if (!failure.empty()) {
                for (std::size_t i = active_.size(); i-- > 0;) retire(i, RequestState::failed, failure);
            }
        }
    }

    for (std::size_t i = active_.size(); i-- > 0;) retire(i, RequestState::failed, failure_.empty() ? "Batch engine stopped" : failure_);
    while (true) {
        std::unique_ptr<WorkItem> item;
        {
            std::lock_guard lock(mutex_);
            if (waiting_.empty()) break;
            item = std::move(waiting_.front()); waiting_.pop_front();
        }
        (void)item->control->finish();
        advance(*item->lifecycle, RequestState::failed);
        item->timings.finished = RequestClock::now();
        item->completion.set_value({item->request.id, RequestState::failed, {}, failure_.empty() ? "Batch engine stopped" : failure_, summarize_timings(item->timings)});
    }
    reap_deliveries(true);
}

} // namespace inferno

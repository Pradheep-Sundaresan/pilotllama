#include "inferno/runtime.hpp"

#include <exception>
#include <stdexcept>
#include <utility>

namespace inferno {
namespace {
void advance(RequestLifecycle& lifecycle, RequestState next) {
    if (!lifecycle.transition_to(next)) {
        throw std::logic_error("Invalid runtime lifecycle transition");
    }
}
} // namespace

Runtime::Runtime(std::unique_ptr<InferenceBackend> backend, std::size_t queue_capacity)
    : backend_(std::move(backend)), queue_(queue_capacity) {
    if (!backend_) {
        throw std::invalid_argument("Runtime requires a backend");
    }
    worker_ = std::thread(&Runtime::work, this);
}

Runtime::~Runtime() {
    shutdown();
}

Submission Runtime::submit(GenerationRequest request, InferenceBackend::TokenCallback token) {
    const auto submitted = RequestClock::now();
    auto item = std::make_unique<WorkItem>();
    item->request = std::move(request);
    item->lifecycle = std::make_shared<RequestLifecycle>();
    item->timings.submitted = submitted;
    item->token = std::move(token);
    item->control = std::make_shared<RequestControl>(item->request.deadline);
    Submission submission{SubmitStatus::invalid, item->lifecycle, item->completion.get_future(), item->control};

    std::lock_guard lock(admission_mutex_);
    std::string error;
    auto immediate_state = RequestState::rejected;
    if (item->request.id == 0 || item->request.max_output_tokens == 0) {
        error = "Require a nonzero ID and output limit";
    } else if (item->request.deadline && RequestClock::now() >= *item->request.deadline) {
        submission.status = SubmitStatus::expired;
        immediate_state = RequestState::timed_out;
        error = "Request deadline exceeded before queue admission";
    } else {
        // The worker takes admission_mutex_ after dequeue, so queued is visible
        // before execution. Rejected requests can still transition from created.
        const auto lifecycle = item->lifecycle;
        auto* published = item.get(); // Borrowed while admission_mutex_ protects publication.
        switch (queue_.try_push(std::move(item))) {
        case PushResult::accepted:
            published->timings.queued = RequestClock::now();
            advance(*lifecycle, RequestState::queued);
            ++waiting_requests_;
            submission.status = SubmitStatus::accepted;
            return submission;
        case PushResult::full:
            submission.status = SubmitStatus::full;
            error = "Request queue is full";
            break;
        case PushResult::closed:
            submission.status = SubmitStatus::closed;
            error = "Runtime is closed";
            break;
        }
    }
    (void) item->control->finish();
    advance(*item->lifecycle, immediate_state);
    item->timings.finished = RequestClock::now();
    item->completion.set_value({item->request.id, immediate_state, {}, std::move(error),
                                summarize_timings(item->timings)});
    return submission;
}

SchedulerSnapshot Runtime::scheduler_snapshot() const {
    std::lock_guard lock(admission_mutex_);
    return {waiting_requests_, executing_requests_, queue_.capacity()};
}

std::optional<MemorySnapshot> Runtime::memory_snapshot() const {
    return backend_->memory_snapshot();
}

void Runtime::shutdown() {
    // Serialize join calls without holding admission_mutex_ while waiting for work.
    std::lock_guard join_lock(shutdown_mutex_);
    {
        std::lock_guard admission_lock(admission_mutex_);
        queue_.close();
    }
    if (worker_.joinable()) {
        worker_.join();
    }
}

void Runtime::work() {
    while (auto pending = queue_.wait_pop()) {
        auto item = std::move(*pending);
        GenerationResult result{item->request.id, RequestState::failed, {}, {}, {}};
        try {
            {
                std::lock_guard lock(admission_mutex_);
                --waiting_requests_;
                executing_requests_ = 1;
                advance(*item->lifecycle, RequestState::admitted);
                item->timings.admitted = RequestClock::now();
            }
            item->control->checkpoint();
            advance(*item->lifecycle, RequestState::prefill);
            item->timings.backend_started = RequestClock::now();
            backend_->generate(item->request, [&] {
                item->control->checkpoint();
                advance(*item->lifecycle, RequestState::decoding);
                item->timings.decode_started = RequestClock::now();
            }, [&](std::string_view piece) {
                item->control->checkpoint();
                if (item->lifecycle->state() != RequestState::decoding ||
                    item->timings.output_tokens >= item->request.max_output_tokens) {
                    throw std::logic_error("Invalid backend token event or output budget exceeded");
                }
                const auto now = RequestClock::now();
                if (!item->timings.first_token) item->timings.first_token = now;
                item->timings.last_token = now;
                ++item->timings.output_tokens;
                result.text.append(piece);
                if (item->token) item->token(piece);
                item->control->checkpoint();
            }, [&] { item->control->checkpoint(); });
            item->timings.backend_finished = RequestClock::now();
            item->control->checkpoint();
            result.state = RequestState::completed;
        } catch (const RequestStopped& stopped) {
            if (item->timings.backend_started && !item->timings.backend_finished) item->timings.backend_finished = RequestClock::now();
            result.state = stopped.state();
            result.error = stopped.what();
        } catch (const std::exception& error) {
            if (item->timings.backend_started && !item->timings.backend_finished) item->timings.backend_finished = RequestClock::now();
            result.error = error.what();
            result.text.clear();
        } catch (...) {
            if (item->timings.backend_started && !item->timings.backend_finished) item->timings.backend_finished = RequestClock::now();
            result.error = "Unknown backend exception";
            result.text.clear();
        }
        const auto stop = item->control->finish();
        if (stop && result.state == RequestState::completed) {
            result.state = *stop;
            result.error = *stop == RequestState::cancelled ? "Request cancelled" : "Request deadline exceeded";
        }
        if (result.state != RequestState::completed) result.text.clear();
        advance(*item->lifecycle, result.state);
        item->timings.finished = RequestClock::now();
        result.metrics = summarize_timings(item->timings);
        {
            std::lock_guard lock(admission_mutex_);
            executing_requests_ = 0;
        }
        item->completion.set_value(std::move(result));
    }
}

} // namespace inferno

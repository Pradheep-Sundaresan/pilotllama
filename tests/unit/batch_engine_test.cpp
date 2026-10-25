#include "inferno/batch_engine.hpp"

#include <gtest/gtest.h>

#include <algorithm>
#include <future>
#include <map>
#include <mutex>
#include <vector>

namespace {
using namespace inferno;

GenerationRequest request(RequestId id, std::string prompt = "three", std::uint32_t output = 32) {
    return {id, std::move(prompt), output, std::nullopt, {}};
}

struct Trace {
    std::mutex mutex;
    std::vector<std::vector<RequestId>> turns;
    std::vector<RequestId> removed;
    std::promise<void> first_step;
    std::promise<void> release_first_promise;
    std::shared_future<void> release_first = release_first_promise.get_future().share();
    std::promise<void> two_active;
    std::promise<void> release_two_promise;
    std::shared_future<void> release_two = release_two_promise.get_future().share();
    bool gate_first = false;
    bool gate_two = false;
    bool fail_remove = false;
    bool invalid_routing = false;
    bool first_announced = false;
    bool two_announced = false;
};

class FakeBatchExecutor final : public BatchExecutor {
public:
    explicit FakeBatchExecutor(std::shared_ptr<Trace> trace) : trace_(std::move(trace)) {}

    std::size_t admit(SequenceId sequence, const GenerationRequest& request) override {
        states_[sequence] = {request.id, 0, request.prompt == "long" ? 6U : 3U, request.enable_thinking};
        return request.prompt == "oversized" ? 2000U : 8U;
    }

    std::vector<BatchStepEvent> step(std::span<const SequenceId> sequences, std::size_t chunk) override {
        EXPECT_EQ(chunk, 128U);
        std::vector<RequestId> ids;
        for (auto sequence : sequences) ids.push_back(states_.at(sequence).id);
        bool first_turn = false;
        {
            std::lock_guard lock(trace_->mutex);
            trace_->turns.push_back(ids);
            if (trace_->gate_first && !trace_->first_announced) {
                trace_->first_announced = true;
                first_turn = true;
                trace_->first_step.set_value();
            }
            if (trace_->gate_two && ids.size() == 2 && !trace_->two_announced) {
                trace_->two_announced = true;
                trace_->two_active.set_value();
            }
        }
        if (trace_->gate_first && first_turn) trace_->release_first.wait();
        if (trace_->gate_two && ids.size() == 2 && trace_->two_announced) {
            trace_->gate_two = false;
            trace_->release_two.wait();
        }
        if (trace_->invalid_routing) return {};

        std::vector<BatchStepEvent> events;
        for (auto sequence : sequences) {
            auto& state = states_.at(sequence);
            const auto number = state.emitted++;
            BatchStepEvent event{sequence, false, false, {}, false, {}};
            event.decode_started = number == 0;
            event.generated_token = true;
            event.output_pieces.push_back({std::to_string(state.id) + ":" + std::to_string(number),
                state.thinking && number == 0 ? OutputChannel::reasoning : OutputChannel::content});
            event.finished = state.emitted == state.tokens;
            events.push_back(std::move(event));
        }
        return events;
    }

    bool remove(SequenceId sequence) noexcept override {
        const auto found = states_.find(sequence);
        if (found != states_.end()) {
            std::lock_guard lock(trace_->mutex);
            trace_->removed.push_back(found->second.id);
            states_.erase(found);
        }
        return !trace_->fail_remove;
    }

private:
    struct State { RequestId id; unsigned emitted; unsigned tokens; bool thinking; };
    std::shared_ptr<Trace> trace_;
    std::map<SequenceId, State> states_;
};

TEST(BatchEngine, RequiresConservativeBoundedConfiguration) {
    EXPECT_THROW(ContinuousBatchEngine(nullptr), std::invalid_argument);
    auto trace = std::make_shared<Trace>();
    BatchEngineConfig config;
    config.active_sequences = 3;
    EXPECT_THROW(ContinuousBatchEngine(std::make_unique<FakeBatchExecutor>(trace), config), std::invalid_argument);
    config = {};
    config.per_sequence_context_tokens = 4096;
    config.max_output_tokens = 1024;
    EXPECT_NO_THROW(ContinuousBatchEngine(std::make_unique<FakeBatchExecutor>(trace), config));
    config.max_output_tokens = 4097;
    EXPECT_THROW(ContinuousBatchEngine(std::make_unique<FakeBatchExecutor>(trace), config), std::invalid_argument);
}

TEST(BatchEngine, InterleavesTwoActiveRequestsAndRoutesTokens) {
    auto trace = std::make_shared<Trace>();
    trace->gate_first = true;
    auto first_step = trace->first_step.get_future();
    ContinuousBatchEngine engine(std::make_unique<FakeBatchExecutor>(trace));
    auto first = engine.submit(request(1));
    first_step.wait();
    auto second = engine.submit(request(2));
    trace->release_first_promise.set_value();
    EXPECT_EQ(first.completion.get().text, "1:01:11:2");
    EXPECT_EQ(second.completion.get().text, "2:02:12:2");
    std::lock_guard lock(trace->mutex);
    EXPECT_TRUE(std::any_of(trace->turns.begin(), trace->turns.end(), [](const auto& turn) {
        return turn == std::vector<RequestId>({1, 2});
    }));
    auto removed = trace->removed;
    std::sort(removed.begin(), removed.end());
    EXPECT_EQ(removed, (std::vector<RequestId>{1, 2}));
}

TEST(BatchEngine, PreservesReasoningChannelThroughBoundedDelivery) {
    auto trace = std::make_shared<Trace>();
    ContinuousBatchEngine engine(std::make_unique<FakeBatchExecutor>(trace));
    auto value = request(1);
    value.enable_thinking = true;
    std::vector<OutputChannel> channels;
    auto submission = engine.submit(std::move(value), [&](GeneratedPiece piece) { channels.push_back(piece.channel); });
    EXPECT_EQ(submission.completion.get().state, RequestState::completed);
    EXPECT_EQ(channels, (std::vector<OutputChannel>{OutputChannel::reasoning, OutputChannel::content, OutputChannel::content}));
}

TEST(BatchEngine, KeepsTwoWaitingSlotsBounded) {
    auto trace = std::make_shared<Trace>();
    trace->gate_first = true;
    auto first_step = trace->first_step.get_future();
    ContinuousBatchEngine engine(std::make_unique<FakeBatchExecutor>(trace));
    auto first = engine.submit(request(1));
    first_step.wait();
    auto second = engine.submit(request(2));
    auto third = engine.submit(request(3));
    auto fourth = engine.submit(request(4));
    EXPECT_EQ(second.status, SubmitStatus::accepted);
    EXPECT_EQ(third.status, SubmitStatus::accepted);
    EXPECT_EQ(fourth.status, SubmitStatus::full);
    EXPECT_EQ(engine.scheduler_snapshot().waiting_requests, 2U);
    EXPECT_EQ(engine.scheduler_snapshot().executing_requests, 1U);
    trace->release_first_promise.set_value();
    EXPECT_EQ(first.completion.get().state, RequestState::completed);
    EXPECT_EQ(second.completion.get().state, RequestState::completed);
    EXPECT_EQ(third.completion.get().state, RequestState::completed);
}

TEST(BatchEngine, CancelsOneSequenceWhileTheOtherCompletes) {
    auto trace = std::make_shared<Trace>();
    trace->gate_first = true;
    trace->gate_two = true;
    auto first_step = trace->first_step.get_future();
    auto two_active = trace->two_active.get_future();
    ContinuousBatchEngine engine(std::make_unique<FakeBatchExecutor>(trace));
    auto first = engine.submit(request(1, "long"));
    first_step.wait();
    auto second = engine.submit(request(2, "long"));
    trace->release_first_promise.set_value();
    two_active.wait();
    EXPECT_EQ(engine.scheduler_snapshot().executing_requests, 2U);
    EXPECT_EQ(engine.scheduler_snapshot().waiting_requests, 0U);
    EXPECT_TRUE(first.control->cancel());
    trace->release_two_promise.set_value();
    EXPECT_EQ(first.completion.get().state, RequestState::cancelled);
    EXPECT_EQ(second.completion.get().state, RequestState::completed);
}

TEST(BatchEngine, RejectsOutputAndContextBounds) {
    auto trace = std::make_shared<Trace>();
    ContinuousBatchEngine engine(std::make_unique<FakeBatchExecutor>(trace));
    auto too_many = engine.submit(request(1, "three", 513));
    EXPECT_EQ(too_many.status, SubmitStatus::invalid);
    EXPECT_EQ(too_many.completion.get().state, RequestState::rejected);
    auto oversized = engine.submit(request(2, "oversized", 64));
    const auto result = oversized.completion.get();
    EXPECT_EQ(result.state, RequestState::failed);
    EXPECT_NE(result.error.find("context bound"), std::string::npos);
}

TEST(BatchEngine, InvalidRoutingFailsClosed) {
    auto trace = std::make_shared<Trace>();
    trace->invalid_routing = true;
    ContinuousBatchEngine engine(std::make_unique<FakeBatchExecutor>(trace));
    EXPECT_EQ(engine.submit(request(1)).completion.get().state, RequestState::failed);
    auto rejected = engine.submit(request(2));
    EXPECT_EQ(rejected.status, SubmitStatus::closed);
}

TEST(BatchEngine, CleanupFailureFailsClosed) {
    auto trace = std::make_shared<Trace>();
    trace->fail_remove = true;
    ContinuousBatchEngine engine(std::make_unique<FakeBatchExecutor>(trace));
    const auto result = engine.submit(request(1)).completion.get();
    EXPECT_EQ(result.state, RequestState::failed);
    EXPECT_NE(result.error.find("cleanup failed"), std::string::npos);
    EXPECT_EQ(engine.submit(request(2)).status, SubmitStatus::closed);
}

TEST(BatchEngine, SlowConsumerCannotStopAnotherSequence) {
    auto trace = std::make_shared<Trace>();
    trace->gate_first = true;
    auto first_step = trace->first_step.get_future();
    ContinuousBatchEngine engine(std::make_unique<FakeBatchExecutor>(trace));
    std::promise<void> consumer_entered;
    std::promise<void> release_consumer;
    auto released = release_consumer.get_future().share();
    auto slow = engine.submit(request(1, "long"), [&](GeneratedPiece) {
        consumer_entered.set_value();
        released.wait();
    });
    first_step.wait();
    auto other = engine.submit(request(2));
    trace->release_first_promise.set_value();
    consumer_entered.get_future().wait();
    EXPECT_EQ(other.completion.wait_for(std::chrono::seconds(1)), std::future_status::ready);
    EXPECT_EQ(other.completion.get().state, RequestState::completed);
    release_consumer.set_value();
    EXPECT_EQ(slow.completion.get().state, RequestState::failed);
}

} // namespace

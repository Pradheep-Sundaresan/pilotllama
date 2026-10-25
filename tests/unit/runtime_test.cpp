#include "inferno/runtime.hpp"

#include <gtest/gtest.h>

#include <atomic>
#include <array>
#include <future>
#include <stdexcept>
#include <utility>
#include <vector>

namespace {
using namespace inferno;

GenerationRequest request(RequestId id, std::string prompt = "hello") {
    return {id, std::move(prompt), 32, std::nullopt, {}};
}

class FakeBackend final : public InferenceBackend {
public:
    std::optional<MemorySnapshot> memory_snapshot() const override {
        MemorySnapshot result;
        result.configured_context_tokens = 4096;
        return result;
    }
    void generate(const GenerationRequest& req, const DecodeStarted& decode, const TokenCallback& token,
                  const Checkpoint& checkpoint) override {
        checkpoint();
        if (req.prompt == "throw") {
            throw std::runtime_error("fake backend failure");
        }
        if (req.prompt == "unknown") {
            throw 7;
        }
        if (req.prompt == "empty") {
            return;
        }
        if (req.prompt == "missing-decode") {
            token("unreported output");
            return;
        }
        decode();
        if (req.prompt == "double-decode") {
            decode();
        }
        if (req.prompt == "pieces") {
            token("one");
            token("");
            token("two");
        } else if (req.prompt == "channels") {
            token({"why", OutputChannel::reasoning});
            token({"answer", OutputChannel::content});
        } else if (req.prompt == "parser-flush") {
            token({"why", OutputChannel::reasoning, true});
            token({" answer", OutputChannel::content, false});
        } else {
            token("fake:" + req.prompt);
        }
    }
};

struct Gate {
    std::promise<void> entered;
    std::promise<void> release;
    std::shared_future<void> released = release.get_future().share();
    std::vector<RequestId> order;
};

class GatedBackend final : public InferenceBackend {
public:
    explicit GatedBackend(std::shared_ptr<Gate> gate) : gate_(std::move(gate)) {}
    void generate(const GenerationRequest& req, const DecodeStarted& decode, const TokenCallback& token,
                  const Checkpoint& checkpoint) override {
        gate_->order.push_back(req.id);
        if (gate_->order.size() == 1) {
            gate_->entered.set_value();
            gate_->released.wait();
        }
        checkpoint();
        decode();
        token("done");
    }
private:
    std::shared_ptr<Gate> gate_;
};

TEST(Runtime, RequiresBackendAndPositiveCapacity) {
    EXPECT_THROW(Runtime(nullptr, 1), std::invalid_argument);
    EXPECT_THROW(Runtime(std::make_unique<FakeBackend>(), 0), std::invalid_argument);
}

TEST(Runtime, ParserFlushDoesNotInventANativeToken) {
    Runtime runtime(std::make_unique<FakeBackend>(), 1);
    std::vector<GeneratedPiece> pieces;
    auto submission = runtime.submit(request(2, "parser-flush"), [&](GeneratedPiece piece) { pieces.push_back(piece); });
    const auto result = submission.completion.get();
    ASSERT_EQ(result.state, RequestState::completed);
    EXPECT_EQ(result.metrics.output_tokens, 1U);
    EXPECT_EQ(result.text, "why answer");
    ASSERT_EQ(pieces.size(), 2U);
    EXPECT_TRUE(pieces[0].advances_output_tokens);
    EXPECT_FALSE(pieces[1].advances_output_tokens);
}

TEST(Runtime, PreservesReasoningAndContentChannels) {
    Runtime runtime(std::make_unique<FakeBackend>(), 1);
    std::vector<OutputChannel> channels;
    auto submission = runtime.submit(request(1, "channels"), [&](GeneratedPiece piece) { channels.push_back(piece.channel); });
    const auto result = submission.completion.get();
    EXPECT_EQ(result.state, RequestState::completed);
    EXPECT_EQ(result.text, "whyanswer");
    EXPECT_EQ(channels, (std::vector<OutputChannel>{OutputChannel::reasoning, OutputChannel::content}));
}

TEST(Runtime, CompletesAndPreservesRequestIdentity) {
    Runtime runtime(std::make_unique<FakeBackend>(), 2);
    const auto memory = runtime.memory_snapshot();
    ASSERT_TRUE(memory);
    EXPECT_EQ(memory->configured_context_tokens, 4096U);
    auto submission = runtime.submit(request(17));
    EXPECT_EQ(submission.status, SubmitStatus::accepted);
    const auto result = submission.completion.get();
    EXPECT_EQ(result.id, 17U);
    EXPECT_EQ(result.state, RequestState::completed);
    EXPECT_EQ(result.text, "fake:hello");
    EXPECT_TRUE(result.error.empty());
    EXPECT_EQ(submission.lifecycle->state(), RequestState::completed);
}

TEST(Runtime, RejectsInvalidRequests) {
    Runtime runtime(std::make_unique<FakeBackend>(), 1);
    auto zero_output = request(1);
    zero_output.max_output_tokens = 0;
    for (auto req : {request(0), zero_output}) {
        auto submission = runtime.submit(req);
        EXPECT_EQ(submission.status, SubmitStatus::invalid);
        const auto result = submission.completion.get();
        EXPECT_EQ(result.state, RequestState::rejected);
        EXPECT_FALSE(result.error.empty());
        EXPECT_EQ(submission.lifecycle->state(), RequestState::rejected);
    }
}

TEST(Runtime, BoundedAdmissionAndFifoExecution) {
    auto gate = std::make_shared<Gate>();
    auto entered = gate->entered.get_future();
    Runtime runtime(std::make_unique<GatedBackend>(gate), 2);
    auto active = runtime.submit(request(1));
    entered.wait();
    EXPECT_EQ(runtime.scheduler_snapshot().waiting_requests, 0U);
    EXPECT_EQ(runtime.scheduler_snapshot().executing_requests, 1U);
    EXPECT_EQ(runtime.scheduler_snapshot().queue_capacity, 2U);
    EXPECT_EQ(active.lifecycle->state(), RequestState::prefill);
    auto second = runtime.submit(request(2));
    auto third = runtime.submit(request(3));
    auto rejected = runtime.submit(request(4));
    EXPECT_EQ(second.status, SubmitStatus::accepted);
    EXPECT_EQ(third.status, SubmitStatus::accepted);
    EXPECT_EQ(second.lifecycle->state(), RequestState::queued);
    EXPECT_EQ(rejected.status, SubmitStatus::full);
    EXPECT_EQ(rejected.completion.get().state, RequestState::rejected);
    EXPECT_EQ(runtime.scheduler_snapshot().waiting_requests, 2U);
    EXPECT_EQ(runtime.scheduler_snapshot().executing_requests, 1U);
    gate->release.set_value();
    runtime.shutdown();
    EXPECT_EQ(runtime.scheduler_snapshot().waiting_requests, 0U);
    EXPECT_EQ(runtime.scheduler_snapshot().executing_requests, 0U);
    EXPECT_EQ(active.completion.get().state, RequestState::completed);
    EXPECT_EQ(second.completion.get().state, RequestState::completed);
    EXPECT_EQ(third.completion.get().state, RequestState::completed);
    EXPECT_EQ(gate->order, (std::vector<RequestId>{1, 2, 3}));
}

TEST(Runtime, BackendFailuresDoNotStopTheWorker) {
    Runtime runtime(std::make_unique<FakeBackend>(), 4);
    for (const auto& prompt : {"throw", "unknown", "double-decode", "missing-decode"}) {
        auto submission = runtime.submit(request(1, prompt));
        const auto result = submission.completion.get();
        EXPECT_EQ(result.state, RequestState::failed);
        EXPECT_FALSE(result.error.empty());
        EXPECT_TRUE(result.text.empty());
        EXPECT_EQ(submission.lifecycle->state(), RequestState::failed);
    }
    EXPECT_EQ(runtime.submit(request(2)).completion.get().state, RequestState::completed);
}

TEST(Runtime, EmptyOutputCanCompleteWithoutDecode) {
    Runtime runtime(std::make_unique<FakeBackend>(), 1);
    auto result = runtime.submit(request(1, "empty")).completion.get();
    EXPECT_EQ(result.state, RequestState::completed);
    EXPECT_TRUE(result.text.empty());
    EXPECT_EQ(result.metrics.output_tokens, 0U);
    EXPECT_FALSE(result.metrics.ttft_ms);
    EXPECT_FALSE(result.metrics.decode_tokens_per_second);
}

TEST(Runtime, ShutdownIsIdempotentAndClosesAdmission) {
    Runtime runtime(std::make_unique<FakeBackend>(), 1);
    runtime.shutdown();
    runtime.shutdown();
    auto rejected = runtime.submit(request(1));
    EXPECT_EQ(rejected.status, SubmitStatus::closed);
    EXPECT_EQ(rejected.completion.get().state, RequestState::rejected);
}

TEST(Runtime, DestructionDrainsAcceptedWorkAndFuturesOutliveRuntime) {
    auto gate = std::make_shared<Gate>();
    auto entered = gate->entered.get_future();
    std::future<GenerationResult> first;
    std::future<GenerationResult> second;
    std::shared_ptr<const RequestLifecycle> lifecycle;
    {
        Runtime runtime(std::make_unique<GatedBackend>(gate), 1);
        auto active = runtime.submit(request(1));
        first = std::move(active.completion);
        lifecycle = active.lifecycle;
        entered.wait();
        second = runtime.submit(request(2)).completion;
        gate->release.set_value();
    }
    EXPECT_EQ(first.get().state, RequestState::completed);
    EXPECT_EQ(second.get().state, RequestState::completed);
    EXPECT_EQ(lifecycle->state(), RequestState::completed);
    EXPECT_EQ(gate->order, (std::vector<RequestId>{1, 2}));
}

TEST(Runtime, ConcurrentSubmittersAndShutdownResolveEverySubmission) {
    Runtime runtime(std::make_unique<FakeBackend>(), 8);
    std::atomic<bool> start{false};
    std::array<std::vector<Submission>, 4> submissions;
    std::array<std::thread, 4> producers;
    for (std::size_t p = 0; p < producers.size(); ++p) {
        producers[p] = std::thread([&, p] {
            start.wait(false);
            for (RequestId i = 1; i <= 32; ++i) {
                submissions[p].push_back(runtime.submit(request(p * 32 + i)));
            }
        });
    }
    std::array<std::thread, 2> closers;
    for (auto& closer : closers) {
        closer = std::thread([&] {
            start.wait(false);
            runtime.shutdown();
        });
    }
    start.store(true);
    start.notify_all();
    for (auto& producer : producers) {
        producer.join();
    }
    for (auto& closer : closers) {
        closer.join();
    }
    for (auto& batch : submissions) {
        EXPECT_EQ(batch.size(), 32U);
        for (auto& submission : batch) {
            const auto expected = submission.status == SubmitStatus::accepted
                ? RequestState::completed : RequestState::rejected;
            EXPECT_EQ(submission.completion.get().state, expected);
            EXPECT_EQ(submission.lifecycle->state(), expected);
            EXPECT_NE(submission.status, SubmitStatus::invalid);
        }
    }
}

TEST(Runtime, StreamsInOrderAndCountsEmptyTokenPieces) {
    Runtime runtime(std::make_unique<FakeBackend>(), 1);
    std::vector<std::string> pieces;
    auto submission = runtime.submit(request(1, "pieces"), [&](GeneratedPiece piece) { pieces.emplace_back(piece.bytes); });
    const auto result = submission.completion.get();
    EXPECT_EQ(pieces, (std::vector<std::string>{"one", "", "two"}));
    EXPECT_EQ(result.text, "onetwo");
    EXPECT_EQ(result.metrics.output_tokens, 3U);
    EXPECT_TRUE(result.metrics.ttft_ms);
    EXPECT_TRUE(result.metrics.mean_inter_token_ms);
    EXPECT_GE(result.metrics.runtime_latency_ms, *result.metrics.ttft_ms);
}

TEST(Runtime, SlowConsumerBackpressuresWithoutAnExtraQueue) {
    Runtime runtime(std::make_unique<FakeBackend>(), 1);
    std::promise<void> first;
    std::promise<void> release;
    auto released = release.get_future().share();
    std::atomic<unsigned> calls{0};
    auto submission = runtime.submit(request(1, "pieces"), [&](GeneratedPiece) {
        if (++calls == 1) {
            first.set_value();
            released.wait();
        }
    });
    first.get_future().wait();
    EXPECT_EQ(calls.load(), 1U);
    EXPECT_EQ(submission.completion.wait_for(std::chrono::seconds(0)), std::future_status::timeout);
    release.set_value();
    EXPECT_EQ(submission.completion.get().state, RequestState::completed);
    EXPECT_EQ(calls.load(), 3U);
}

TEST(Runtime, CallbackFailureIsTerminalButWorkerRemainsUsable) {
    Runtime runtime(std::make_unique<FakeBackend>(), 1);
    auto failed = runtime.submit(request(1, "pieces"), [](GeneratedPiece) {
        throw std::runtime_error("consumer disconnected");
    }).completion.get();
    EXPECT_EQ(failed.state, RequestState::failed);
    EXPECT_EQ(failed.error, "consumer disconnected");
    EXPECT_TRUE(failed.text.empty());
    EXPECT_EQ(failed.metrics.output_tokens, 1U);
    EXPECT_TRUE(failed.metrics.ttft_ms);
    EXPECT_EQ(runtime.submit(request(2)).completion.get().state, RequestState::completed);
}

TEST(Runtime, EnforcesTokenEventBudget) {
    Runtime runtime(std::make_unique<FakeBackend>(), 1);
    auto req = request(1, "pieces");
    req.max_output_tokens = 2;
    unsigned calls = 0;
    const auto failed = runtime.submit(req, [&](GeneratedPiece) { ++calls; }).completion.get();
    EXPECT_EQ(failed.state, RequestState::failed);
    EXPECT_EQ(calls, 2U);
    EXPECT_EQ(failed.metrics.output_tokens, 2U);
}

TEST(Runtime, ExpiredDeadlineCompletesWithoutBackendWork) {
    Runtime runtime(std::make_unique<FakeBackend>(), 1);
    auto req = request(1);
    req.deadline = RequestClock::now() - std::chrono::seconds(1);
    auto submission = runtime.submit(req);
    EXPECT_EQ(submission.status, SubmitStatus::expired);
    const auto result = submission.completion.get();
    EXPECT_EQ(result.state, RequestState::timed_out);
    EXPECT_EQ(result.metrics.output_tokens, 0U);
    EXPECT_FALSE(result.metrics.backend_execution_ms);
    EXPECT_FALSE(submission.control->cancel());
}

TEST(Runtime, FutureDeadlineAllowsNormalCompletion) {
    Runtime runtime(std::make_unique<FakeBackend>(), 1);
    auto req = request(1);
    req.deadline = RequestClock::now() + std::chrono::hours(1);
    auto submission = runtime.submit(req);
    EXPECT_EQ(submission.status, SubmitStatus::accepted);
    EXPECT_EQ(submission.completion.get().state, RequestState::completed);
    EXPECT_FALSE(submission.control->cancel());
}

TEST(Runtime, QueuedCancellationSkipsBackendAndRetainsSlotUntilDequeue) {
    auto gate = std::make_shared<Gate>();
    auto entered = gate->entered.get_future();
    Runtime runtime(std::make_unique<GatedBackend>(gate), 1);
    auto active = runtime.submit(request(1));
    entered.wait();
    auto queued = runtime.submit(request(2));
    EXPECT_EQ(queued.control->request_cancel(), RequestControl::CancelStatus::requested);
    EXPECT_EQ(queued.control->request_cancel(), RequestControl::CancelStatus::already_requested);
    EXPECT_FALSE(queued.control->cancel());
    EXPECT_EQ(queued.lifecycle->state(), RequestState::queued);
    EXPECT_EQ(runtime.submit(request(3)).status, SubmitStatus::full);
    gate->release.set_value();
    EXPECT_EQ(active.completion.get().state, RequestState::completed);
    const auto result = queued.completion.get();
    EXPECT_EQ(result.state, RequestState::cancelled);
    EXPECT_EQ(queued.control->request_cancel(), RequestControl::CancelStatus::finished);
    EXPECT_FALSE(result.metrics.backend_execution_ms);
    EXPECT_EQ(gate->order, (std::vector<RequestId>{1}));
}

TEST(Runtime, ActivePrefillCancellationIsCooperative) {
    auto gate = std::make_shared<Gate>();
    auto entered = gate->entered.get_future();
    Runtime runtime(std::make_unique<GatedBackend>(gate), 1);
    auto active = runtime.submit(request(1));
    entered.wait();
    EXPECT_TRUE(active.control->cancel());
    gate->release.set_value();
    const auto result = active.completion.get();
    EXPECT_EQ(result.state, RequestState::cancelled);
    EXPECT_EQ(result.metrics.output_tokens, 0U);
    EXPECT_FALSE(result.metrics.ttft_ms);
    EXPECT_TRUE(result.metrics.backend_execution_ms);
    EXPECT_FALSE(active.control->cancel());
}

TEST(Runtime, TokenCallbackCanRequestCancellation) {
    Runtime runtime(std::make_unique<FakeBackend>(), 1);
    std::promise<std::shared_ptr<RequestControl>> published;
    auto handle = published.get_future().share();
    unsigned calls = 0;
    auto active = runtime.submit(request(1, "pieces"), [&](GeneratedPiece) {
        ++calls;
        (void) handle.get()->cancel();
    });
    published.set_value(active.control);
    const auto result = active.completion.get();
    EXPECT_EQ(result.state, RequestState::cancelled);
    EXPECT_EQ(calls, 1U);
    EXPECT_EQ(result.metrics.output_tokens, 1U);
    EXPECT_TRUE(result.text.empty());
    EXPECT_EQ(runtime.submit(request(2)).completion.get().state, RequestState::completed);
}

TEST(Runtime, QueuedDeadlineExpiresBeforeBackendExecution) {
    auto gate = std::make_shared<Gate>();
    auto entered = gate->entered.get_future();
    Runtime runtime(std::make_unique<GatedBackend>(gate), 1);
    auto active = runtime.submit(request(1));
    entered.wait();
    auto req = request(2);
    req.deadline = RequestClock::now() + std::chrono::milliseconds(250);
    auto queued = runtime.submit(req);
    EXPECT_EQ(queued.status, SubmitStatus::accepted);
    std::mutex mutex;
    std::condition_variable clock_wait;
    std::unique_lock lock(mutex);
    clock_wait.wait_until(lock, *req.deadline, [] { return false; });
    gate->release.set_value();
    EXPECT_EQ(active.completion.get().state, RequestState::completed);
    const auto result = queued.completion.get();
    EXPECT_EQ(result.state, RequestState::timed_out);
    EXPECT_FALSE(result.metrics.backend_execution_ms);
    EXPECT_EQ(gate->order, (std::vector<RequestId>{1}));
}

TEST(Runtime, CancellationRacingSuccessfulCompletionHasOneTerminalOutcome) {
    Runtime runtime(std::make_unique<FakeBackend>(), 1);
    for (RequestId id = 1; id <= 16; ++id) {
        auto submission = runtime.submit(request(id));
        bool cancelled = false;
        std::thread canceller([&] { cancelled = submission.control->cancel(); });
        const auto result = submission.completion.get();
        canceller.join();
        EXPECT_EQ(result.state, cancelled ? RequestState::cancelled : RequestState::completed);
        EXPECT_EQ(submission.lifecycle->state(), result.state);
        EXPECT_FALSE(submission.control->cancel());
    }
}
} // namespace

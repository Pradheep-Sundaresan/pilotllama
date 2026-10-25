#include "inferno/llama_backend.hpp"
#include "inferno/runtime.hpp"
#include <gtest/gtest.h>
#include <cstdlib>
#include <stdexcept>

TEST(LlamaBackendIntegration, WorkerGeneratesAndRecoversAfterOversizeRequest) {
    const auto* model = std::getenv("INFERNO_TEST_MODEL");
    ASSERT_NE(model, nullptr);
    inferno::Runtime runtime(inferno::make_llama_backend({model}), 2);
    std::string streamed;
    unsigned events = 0;
    auto first = runtime.submit({1, "What is 2 + 2? Reply with just the number.", 1, {}},
                               [&](inferno::GeneratedPiece piece) { streamed += piece.bytes; ++events; });
    auto answer = first.completion.get();
    EXPECT_EQ(answer.state, inferno::RequestState::completed) << answer.error;
    EXPECT_EQ(answer.text, "4");
    EXPECT_EQ(streamed, answer.text);
    EXPECT_EQ(events, 1U);
    EXPECT_EQ(answer.metrics.output_tokens, events);
    EXPECT_TRUE(answer.metrics.ttft_ms);
    EXPECT_FALSE(answer.metrics.mean_inter_token_ms);
    EXPECT_EQ(first.lifecycle->state(), inferno::RequestState::completed);
    std::string oversized;
    for (int i = 0; i < 5000; ++i) {
        oversized += "hello ";
    }
    auto failed = runtime.submit({2, oversized, 64, {}}).completion.get();
    EXPECT_EQ(failed.state, inferno::RequestState::failed);
    EXPECT_NE(failed.error.find("exceeds configured context"), std::string::npos);
    auto disconnected = runtime.submit({4, "What is 9 + 9? Reply with just the number.", 4, {}},
                                      [](inferno::GeneratedPiece) { throw std::runtime_error("stop stream"); }).completion.get();
    EXPECT_EQ(disconnected.state, inferno::RequestState::failed);
    EXPECT_EQ(disconnected.metrics.output_tokens, 1U);
    EXPECT_EQ(disconnected.error, "stop stream");
    std::promise<std::shared_ptr<inferno::RequestControl>> published;
    auto control = published.get_future().share();
    auto cancelled = runtime.submit({5, "Explain what a CPU does in one short sentence.", 64, {}},
                                    [&](inferno::GeneratedPiece) { (void) control.get()->cancel(); });
    published.set_value(cancelled.control);
    const auto stopped = cancelled.completion.get();
    EXPECT_EQ(stopped.state, inferno::RequestState::cancelled);
    EXPECT_EQ(stopped.metrics.output_tokens, 1U);
    inferno::GenerationRequest deadline{6, "Explain what a CPU does in one short sentence.", 64,
                                      inferno::RequestClock::now() + std::chrono::milliseconds(1)};
    EXPECT_EQ(runtime.submit(deadline).completion.get().state, inferno::RequestState::timed_out);
    auto second = runtime.submit({3, "What is 3 + 3? Reply with just the number.", 16, {}}).completion.get();
    EXPECT_EQ(second.state, inferno::RequestState::completed) << second.error;
    EXPECT_EQ(second.text, "6");
}

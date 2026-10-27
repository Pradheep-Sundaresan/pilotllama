#include "inferno/metrics.hpp"
#include <gtest/gtest.h>
#include <stdexcept>

namespace {
auto at(int ms) { return inferno::RequestClock::time_point{} + std::chrono::milliseconds(ms); }

TEST(RequestMetrics, UsesDocumentedTimingBoundaries) {
    inferno::RequestTimings t;
    t.submitted = at(0); t.queued = at(2); t.admitted = at(12);
    t.backend_started = at(15); t.decode_started = at(35);
    t.first_token = at(40); t.last_token = at(60); t.backend_finished = at(75); t.finished = at(80); t.output_tokens = 3;
    const auto m = inferno::summarize_timings(t);
    EXPECT_DOUBLE_EQ(m.runtime_latency_ms, 80);
    EXPECT_EQ(m.queue_wait_ms, 10);
    EXPECT_EQ(m.backend_execution_ms, 60);
    EXPECT_EQ(m.prefill_phase_ms, 20);
    EXPECT_EQ(m.decode_phase_ms, 40);
    EXPECT_EQ(m.ttft_ms, 40);
    EXPECT_EQ(m.mean_inter_token_ms, 10);
    EXPECT_EQ(m.decode_tokens_per_second, 75);
}

TEST(RequestMetrics, MissingEventsStayNullAndOneTokenHasNoInterval) {
    inferno::RequestTimings t;
    t.submitted = at(0); t.finished = at(5);
    auto m = inferno::summarize_timings(t);
    EXPECT_FALSE(m.ttft_ms);
    EXPECT_FALSE(m.queue_wait_ms);
    EXPECT_FALSE(m.decode_tokens_per_second);
    EXPECT_NE(inferno::metrics_json(1, inferno::RequestState::rejected, m).find("\"ttft_ms\":null"), std::string::npos);
    t.decode_started = at(1); t.first_token = at(3); t.last_token = at(3); t.output_tokens = 1;
    m = inferno::summarize_timings(t);
    EXPECT_FALSE(m.mean_inter_token_ms);
    EXPECT_EQ(m.ttft_ms, 3);
}

TEST(RequestMetrics, SpeculativeMeasurementsRemainExplicit) {
    inferno::RequestMetrics m;
    m.speculative_drafted_tokens = 8;
    m.speculative_accepted_tokens = 5;
    m.speculative_acceptance_rate = 0.625;
    const auto json = inferno::metrics_json(1, inferno::RequestState::completed, m);
    EXPECT_NE(json.find("\"speculative_drafted_tokens\":8"), std::string::npos);
    EXPECT_NE(json.find("\"speculative_accepted_tokens\":5"), std::string::npos);
    EXPECT_NE(json.find("\"speculative_acceptance_rate\":0.625"), std::string::npos);
}

TEST(RequestMetrics, ZeroDurationDoesNotProduceInfiniteRate) {
    inferno::RequestTimings t;
    t.submitted = at(0); t.decode_started = at(0); t.backend_finished = at(0); t.finished = at(0); t.output_tokens = 1;
    EXPECT_FALSE(inferno::summarize_timings(t).decode_tokens_per_second);
}

TEST(RequestMetrics, RejectsBackwardTimestamps) {
    inferno::RequestTimings t;
    t.submitted = at(10); t.finished = at(0);
    EXPECT_THROW((void) inferno::summarize_timings(t), std::invalid_argument);
}
} // namespace

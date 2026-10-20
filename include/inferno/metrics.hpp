#pragma once
#include "inferno/request.hpp"

namespace inferno {
struct RequestTimings {
    RequestClock::time_point submitted;
    std::optional<RequestClock::time_point> queued, admitted, backend_started, backend_finished, decode_started;
    std::optional<RequestClock::time_point> first_token, last_token;
    RequestClock::time_point finished;
    std::uint32_t output_tokens = 0;
};

struct RequestMetrics {
    std::uint32_t output_tokens = 0;
    double runtime_latency_ms = 0;
    std::optional<double> queue_wait_ms, backend_execution_ms, prefill_phase_ms, decode_phase_ms;
    std::optional<double> ttft_ms, mean_inter_token_ms, decode_tokens_per_second;
};

[[nodiscard]] RequestMetrics summarize_timings(const RequestTimings& timings);
[[nodiscard]] std::string metrics_json(RequestId id, RequestState state, const RequestMetrics& metrics);
} // namespace inferno

#include "inferno/metrics.hpp"
#include <iomanip>
#include <locale>
#include <sstream>
#include <stdexcept>

namespace inferno {
namespace {
double elapsed(RequestClock::time_point start, RequestClock::time_point end) {
    if (end < start) {
        throw std::invalid_argument("Request timestamps must be monotonic");
    }
    return std::chrono::duration<double, std::milli>(end - start).count();
}
} // namespace

RequestMetrics summarize_timings(const RequestTimings& t) {
    RequestMetrics m;
    m.output_tokens = t.output_tokens;
    m.runtime_latency_ms = elapsed(t.submitted, t.finished);
    if (t.queued && t.admitted) m.queue_wait_ms = elapsed(*t.queued, *t.admitted);
    if (t.backend_started && t.backend_finished) m.backend_execution_ms = elapsed(*t.backend_started, *t.backend_finished);
    if (t.backend_started && t.decode_started) m.prefill_phase_ms = elapsed(*t.backend_started, *t.decode_started);
    if (t.decode_started && t.backend_finished) {
        m.decode_phase_ms = elapsed(*t.decode_started, *t.backend_finished);
        if (t.output_tokens && *m.decode_phase_ms > 0) {
            m.decode_tokens_per_second = 1000.0 * t.output_tokens / *m.decode_phase_ms;
        }
    }
    if (t.first_token) m.ttft_ms = elapsed(t.submitted, *t.first_token);
    if (t.output_tokens > 1 && t.first_token && t.last_token) {
        m.mean_inter_token_ms = elapsed(*t.first_token, *t.last_token) / (t.output_tokens - 1);
    }
    return m;
}

std::string metrics_json(RequestId id, RequestState state, const RequestMetrics& m) {
    std::ostringstream out;
    out.imbue(std::locale::classic());
    out << std::setprecision(10) << "{\"request_id\":" << id << ",\"state\":\"" << to_string(state)
        << "\",\"output_tokens\":" << m.output_tokens << ",\"runtime_latency_ms\":" << m.runtime_latency_ms;
    const auto field = [&](std::string_view name, const std::optional<double>& value) {
        out << ",\"" << name << "\":";
        if (value) out << *value;
        else out << "null";
    };
    field("queue_wait_ms", m.queue_wait_ms);
    field("backend_execution_ms", m.backend_execution_ms);
    field("prefill_phase_ms", m.prefill_phase_ms);
    field("decode_phase_ms", m.decode_phase_ms);
    field("ttft_ms", m.ttft_ms);
    field("mean_inter_token_ms", m.mean_inter_token_ms);
    field("decode_tokens_per_second", m.decode_tokens_per_second);
    out << ",\"speculative_drafted_tokens\":";
    if (m.speculative_drafted_tokens) out << *m.speculative_drafted_tokens; else out << "null";
    out << ",\"speculative_accepted_tokens\":";
    if (m.speculative_accepted_tokens) out << *m.speculative_accepted_tokens; else out << "null";
    field("speculative_acceptance_rate", m.speculative_acceptance_rate);
    out << '}';
    return out.str();
}
} // namespace inferno

#pragma once

#include <atomic>
#include <chrono>
#include <cstdint>
#include <optional>
#include <string>
#include <string_view>

namespace inferno {

using RequestId = std::uint64_t;
using RequestClock = std::chrono::steady_clock;

struct GenerationRequest {
    RequestId id = 0;
    std::string prompt;
    std::uint32_t max_output_tokens = 128;
    std::optional<RequestClock::time_point> deadline;
};

enum class RequestState {
    created, queued, admitted, prefill, decoding,
    completed, cancelled, failed, timed_out, rejected
};

[[nodiscard]] bool is_terminal(RequestState state) noexcept;
[[nodiscard]] bool can_transition(RequestState from, RequestState to) noexcept;
[[nodiscard]] std::string_view to_string(RequestState state) noexcept;

// State transitions are atomic; synchronization of request data remains the owner's job.
class RequestLifecycle {
public:
    [[nodiscard]] RequestState state() const noexcept;
    [[nodiscard]] bool transition_to(RequestState next) noexcept;

private:
    std::atomic<RequestState> state_{RequestState::created};
};

} // namespace inferno

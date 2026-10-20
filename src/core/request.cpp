#include "inferno/request.hpp"

namespace inferno {

bool is_terminal(RequestState state) noexcept {
    switch (state) {
    case RequestState::completed:
    case RequestState::cancelled:
    case RequestState::failed:
    case RequestState::timed_out:
    case RequestState::rejected:
        return true;
    default:
        return false;
    }
}

bool can_transition(RequestState from, RequestState to) noexcept {
    switch (from) {
    case RequestState::created:
    case RequestState::queued:
    case RequestState::admitted:
    case RequestState::prefill:
    case RequestState::decoding:
        break;
    default:
        return false;
    }
    if (to == RequestState::cancelled || to == RequestState::failed || to == RequestState::timed_out) {
        return true;
    }
    switch (from) {
    case RequestState::created:
        return to == RequestState::queued || to == RequestState::rejected;
    case RequestState::queued:
        return to == RequestState::admitted;
    case RequestState::admitted:
        return to == RequestState::prefill;
    case RequestState::prefill:
        return to == RequestState::decoding || to == RequestState::completed;
    case RequestState::decoding:
        return to == RequestState::completed;
    default:
        return false;
    }
}

std::string_view to_string(RequestState state) noexcept {
    switch (state) {
    case RequestState::created: return "created";
    case RequestState::queued: return "queued";
    case RequestState::admitted: return "admitted";
    case RequestState::prefill: return "prefill";
    case RequestState::decoding: return "decoding";
    case RequestState::completed: return "completed";
    case RequestState::cancelled: return "cancelled";
    case RequestState::failed: return "failed";
    case RequestState::timed_out: return "timed_out";
    case RequestState::rejected: return "rejected";
    }
    return "unknown";
}

RequestState RequestLifecycle::state() const noexcept {
    return state_.load();
}

bool RequestLifecycle::transition_to(RequestState next) noexcept {
    auto current = state_.load();
    while (can_transition(current, next)) {
        if (state_.compare_exchange_weak(current, next)) {
            return true;
        }
    }
    return false;
}

} // namespace inferno

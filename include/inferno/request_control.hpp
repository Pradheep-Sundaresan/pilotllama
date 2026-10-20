#pragma once
#include "inferno/request.hpp"
#include <exception>
#include <mutex>

namespace inferno {
class RequestStopped final : public std::exception {
public:
    enum class Reason { cancelled, timed_out };
    explicit RequestStopped(Reason reason) : reason_(reason) {}
    [[nodiscard]] RequestState state() const noexcept {
        return reason_ == Reason::cancelled ? RequestState::cancelled : RequestState::timed_out;
    }
    const char* what() const noexcept override {
        return reason_ == Reason::cancelled ? "Request cancelled" : "Request deadline exceeded";
    }
private:
    Reason reason_;
};

class RequestControl {
public:
    enum class CancelStatus { requested, already_requested, finished };
    explicit RequestControl(std::optional<RequestClock::time_point> deadline) : deadline_(deadline) {}
    // Records intent only. False means already requested or completion is sealed.
    [[nodiscard]] bool cancel() {
        return request_cancel() == CancelStatus::requested;
    }
    [[nodiscard]] CancelStatus request_cancel() {
        std::lock_guard lock(mutex_);
        if (finished_) return CancelStatus::finished;
        if (cancelled_) return CancelStatus::already_requested;
        cancelled_ = true;
        return CancelStatus::requested;
    }
    void checkpoint() const {
        if (cancelled_.load()) throw RequestStopped(RequestStopped::Reason::cancelled);
        if (deadline_ && RequestClock::now() >= *deadline_) {
            throw RequestStopped(RequestStopped::Reason::timed_out);
        }
    }
private:
    friend class Runtime;
    friend class ContinuousBatchEngine;
    [[nodiscard]] std::optional<RequestState> finish() {
        std::lock_guard lock(mutex_);
        finished_ = true;
        if (cancelled_.load()) return RequestState::cancelled;
        if (deadline_ && RequestClock::now() >= *deadline_) return RequestState::timed_out;
        return std::nullopt;
    }
    const std::optional<RequestClock::time_point> deadline_;
    std::atomic<bool> cancelled_{false};
    mutable std::mutex mutex_;
    bool finished_ = false;
};
} // namespace inferno

#include "inferno/request.hpp"

#include <gtest/gtest.h>

#include <array>
#include <atomic>
#include <thread>

namespace {
using inferno::RequestState;

TEST(RequestLifecycle, ChecksEveryStatePair) {
    constexpr std::array<unsigned, 10> allowed{
        (1U << 1) | (1U << 6) | (1U << 7) | (1U << 8) | (1U << 9),
        (1U << 2) | (1U << 6) | (1U << 7) | (1U << 8),
        (1U << 3) | (1U << 6) | (1U << 7) | (1U << 8),
        (1U << 4) | (1U << 5) | (1U << 6) | (1U << 7) | (1U << 8),
        (1U << 5) | (1U << 6) | (1U << 7) | (1U << 8),
        0, 0, 0, 0, 0
    };
    for (unsigned from = 0; from < allowed.size(); ++from) {
        for (unsigned to = 0; to < allowed.size(); ++to) {
            EXPECT_EQ(inferno::can_transition(static_cast<RequestState>(from), static_cast<RequestState>(to)),
                      (allowed[from] & (1U << to)) != 0) << from << " -> " << to;
        }
        EXPECT_EQ(inferno::is_terminal(static_cast<RequestState>(from)), from >= 5);
    }
}

TEST(RequestLifecycle, InvalidTransitionPreservesStateAndCompletionIsFinal) {
    inferno::RequestLifecycle request;
    EXPECT_EQ(request.state(), RequestState::created);
    EXPECT_FALSE(request.transition_to(RequestState::decoding));
    EXPECT_EQ(request.state(), RequestState::created);
    for (auto next : {RequestState::queued, RequestState::admitted, RequestState::prefill,
                      RequestState::decoding, RequestState::completed}) {
        EXPECT_TRUE(request.transition_to(next));
        EXPECT_EQ(request.state(), next);
    }
    EXPECT_FALSE(request.transition_to(RequestState::cancelled));
    EXPECT_EQ(request.state(), RequestState::completed);
}

TEST(RequestLifecycle, AllowsCompletionWithoutOutputTokens) {
    inferno::RequestLifecycle request;
    EXPECT_TRUE(request.transition_to(RequestState::queued));
    EXPECT_TRUE(request.transition_to(RequestState::admitted));
    EXPECT_TRUE(request.transition_to(RequestState::prefill));
    EXPECT_TRUE(request.transition_to(RequestState::completed));
}

TEST(RequestLifecycle, OnlyOneRacingTerminalTransitionSucceeds) {
    inferno::RequestLifecycle request;
    std::atomic<unsigned> successes{0};
    std::atomic<bool> start{false};
    std::array<std::thread, 3> workers;
    constexpr std::array states{RequestState::cancelled, RequestState::failed, RequestState::timed_out};
    for (std::size_t i = 0; i < workers.size(); ++i) {
        workers[i] = std::thread([&, i] {
            start.wait(false);
            if (request.transition_to(states[i])) {
                ++successes;
            }
        });
    }
    start.store(true);
    start.notify_all();
    for (auto& worker : workers) {
        worker.join();
    }
    EXPECT_EQ(successes.load(), 1U);
    EXPECT_TRUE(inferno::is_terminal(request.state()));
}

TEST(RequestLifecycle, NamesAreStable) {
    EXPECT_EQ(inferno::to_string(RequestState::timed_out), "timed_out");
    EXPECT_EQ(inferno::to_string(RequestState::rejected), "rejected");
}

TEST(RequestLifecycle, RejectsUnknownStates) {
    const auto unknown = static_cast<RequestState>(99);
    EXPECT_FALSE(inferno::can_transition(unknown, RequestState::cancelled));
    EXPECT_FALSE(inferno::can_transition(RequestState::created, unknown));
    inferno::RequestLifecycle request;
    EXPECT_FALSE(request.transition_to(unknown));
    EXPECT_EQ(request.state(), RequestState::created);
}
} // namespace

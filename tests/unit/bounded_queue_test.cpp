#include "inferno/bounded_queue.hpp"

#include <gtest/gtest.h>

#include <array>
#include <atomic>
#include <future>
#include <memory>
#include <thread>
#include <vector>

namespace {
using inferno::PushResult;

TEST(BoundedQueue, RejectsZeroCapacity) {
    EXPECT_THROW(inferno::BoundedQueue<int> queue(0), std::invalid_argument);
}

TEST(BoundedQueue, EnforcesCapacityAndWrapsInFifoOrder) {
    inferno::BoundedQueue<int> queue(2);
    EXPECT_EQ(queue.capacity(), 2U);
    EXPECT_EQ(queue.try_push(1), PushResult::accepted);
    EXPECT_EQ(queue.try_push(2), PushResult::accepted);
    EXPECT_EQ(queue.try_push(3), PushResult::full);
    EXPECT_EQ(queue.size(), 2U);
    EXPECT_EQ(queue.wait_pop(), 1);
    EXPECT_EQ(queue.try_push(3), PushResult::accepted);
    EXPECT_EQ(queue.wait_pop(), 2);
    EXPECT_EQ(queue.wait_pop(), 3);
    queue.close();
    EXPECT_EQ(queue.wait_pop(), std::nullopt);
}

TEST(BoundedQueue, CloseDrainsAndIsIdempotent) {
    inferno::BoundedQueue<int> queue(2);
    EXPECT_EQ(queue.try_push(1), PushResult::accepted);
    queue.close();
    queue.close();
    EXPECT_EQ(queue.try_push(2), PushResult::closed);
    EXPECT_EQ(queue.wait_pop(), 1);
    EXPECT_EQ(queue.wait_pop(), std::nullopt);
}

TEST(BoundedQueue, MovesOwnershipOnlyOnAcceptedPush) {
    inferno::BoundedQueue<std::unique_ptr<int>> queue(1);
    auto first = std::make_unique<int>(7);
    EXPECT_EQ(queue.try_push(std::move(first)), PushResult::accepted);
    EXPECT_EQ(first, nullptr);
    auto second = std::make_unique<int>(9);
    EXPECT_EQ(queue.try_push(std::move(second)), PushResult::full);
    ASSERT_NE(second, nullptr);
    auto popped = queue.wait_pop();
    ASSERT_TRUE(popped.has_value());
    EXPECT_EQ(**popped, 7);
    queue.close();
    EXPECT_EQ(queue.try_push(std::move(second)), PushResult::closed);
    EXPECT_NE(second, nullptr);
}

TEST(BoundedQueue, ConsumerReceivesAnItemFromAnotherThread) {
    inferno::BoundedQueue<int> queue(1);
    std::promise<void> started;
    std::optional<int> received;
    std::thread consumer([&] {
        started.set_value();
        received = queue.wait_pop();
    });
    started.get_future().wait();
    EXPECT_EQ(queue.try_push(42), PushResult::accepted);
    queue.close();
    consumer.join();
    EXPECT_EQ(received, 42);
}

TEST(BoundedQueue, ClosingEmptyQueueReleasesAllConsumers) {
    inferno::BoundedQueue<int> queue(1);
    std::atomic<unsigned> finished{0};
    std::array<std::thread, 4> consumers;
    for (auto& consumer : consumers) {
        consumer = std::thread([&] {
            if (!queue.wait_pop()) {
                ++finished;
            }
        });
    }
    queue.close();
    for (auto& consumer : consumers) {
        consumer.join();
    }
    EXPECT_EQ(finished.load(), consumers.size());
}

TEST(BoundedQueue, ConcurrentProducersDeliverEveryAcceptedItemExactlyOnce) {
    constexpr int producers = 4;
    constexpr int per_producer = 100;
    inferno::BoundedQueue<int> queue(producers * per_producer);
    std::array<std::thread, producers> writers;
    std::atomic<unsigned> failures{0};
    std::vector<int> received;
    std::thread reader([&] {
        while (auto item = queue.wait_pop()) {
            received.push_back(*item);
        }
    });
    for (int p = 0; p < producers; ++p) {
        writers[p] = std::thread([&, p] {
            for (int i = 0; i < per_producer; ++i) {
                int value = p * per_producer + i;
                if (queue.try_push(std::move(value)) != PushResult::accepted) {
                    ++failures;
                }
            }
        });
    }
    for (auto& writer : writers) {
        writer.join();
    }
    queue.close();
    reader.join();
    EXPECT_EQ(failures.load(), 0U);
    ASSERT_EQ(received.size(), producers * per_producer);
    std::array<bool, producers * per_producer> seen{};
    std::array<int, producers> last{-1, -1, -1, -1};
    for (const int value : received) {
        ASSERT_GE(value, 0);
        ASSERT_LT(value, producers * per_producer);
        EXPECT_FALSE(seen[value]);
        seen[value] = true;
        const int producer = value / per_producer;
        EXPECT_GT(value, last[producer]);
        last[producer] = value;
    }
}

TEST(BoundedQueue, DestructionReleasesPendingOwnership) {
    auto owner = std::make_shared<int>(5);
    std::weak_ptr<int> observer = owner;
    {
        inferno::BoundedQueue<std::shared_ptr<int>> queue(1);
        EXPECT_EQ(queue.try_push(std::move(owner)), PushResult::accepted);
        EXPECT_FALSE(observer.expired());
        queue.close();
    }
    EXPECT_TRUE(observer.expired());
}

TEST(BoundedQueue, ConcurrentCloseDrainsExactlyTheAcceptedItems) {
    inferno::BoundedQueue<int> queue(128);
    std::atomic<bool> start{false};
    unsigned accepted = 0;
    unsigned rejected = 0;
    std::thread producer([&] {
        start.wait(false);
        for (int i = 0; i < 128; ++i) {
            const auto result = queue.try_push(std::move(i));
            if (result == PushResult::accepted) {
                ++accepted;
            } else {
                ++rejected;
            }
        }
    });
    std::thread closer([&] {
        start.wait(false);
        queue.close();
    });
    start.store(true);
    start.notify_all();
    producer.join();
    closer.join();
    unsigned drained = 0;
    while (queue.wait_pop()) {
        ++drained;
    }
    EXPECT_EQ(drained, accepted);
    EXPECT_EQ(accepted + rejected, 128U);
    EXPECT_EQ(queue.try_push(999), PushResult::closed);
}
} // namespace

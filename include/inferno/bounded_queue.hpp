#pragma once

#include <condition_variable>
#include <cstddef>
#include <mutex>
#include <optional>
#include <stdexcept>
#include <type_traits>
#include <utility>
#include <vector>

namespace inferno {

enum class PushResult { accepted, full, closed };

// Capacity bounds item count, not memory owned by each item. Close and join users
// before destruction. Close rejects new items and lets consumers drain accepted items.
template <typename T>
class BoundedQueue {
    static_assert(std::is_nothrow_move_constructible_v<T>,
                  "Queue items must support noexcept move construction");

public:
    explicit BoundedQueue(std::size_t capacity) : slots_(capacity) {
        if (capacity == 0) {
            throw std::invalid_argument("Queue capacity must be positive");
        }
    }

    BoundedQueue(const BoundedQueue&) = delete;
    BoundedQueue& operator=(const BoundedQueue&) = delete;

    // Rejected pushes leave the caller's item intact.
    [[nodiscard]] PushResult try_push(T&& item) {
        {
            std::lock_guard lock(mutex_);
            if (closed_) {
                return PushResult::closed;
            }
            if (size_ == slots_.size()) {
                return PushResult::full;
            }
            slots_[(head_ + size_) % slots_.size()].emplace(std::move(item));
            ++size_;
        }
        ready_.notify_one();
        return PushResult::accepted;
    }

    // Empty means closed and fully drained, never temporarily empty.
    [[nodiscard]] std::optional<T> wait_pop() {
        std::unique_lock lock(mutex_);
        ready_.wait(lock, [this] { return closed_ || size_ != 0; });
        if (size_ == 0) {
            return std::nullopt;
        }
        auto item = std::move(slots_[head_]);
        slots_[head_].reset();
        head_ = (head_ + 1) % slots_.size();
        --size_;
        return item;
    }

    void close() {
        {
            std::lock_guard lock(mutex_);
            closed_ = true;
        }
        ready_.notify_all();
    }

    [[nodiscard]] std::size_t size() const {
        std::lock_guard lock(mutex_);
        return size_;
    }

    [[nodiscard]] std::size_t capacity() const noexcept { return slots_.size(); }

private:
    std::vector<std::optional<T>> slots_;
    mutable std::mutex mutex_;
    std::condition_variable ready_;
    std::size_t head_ = 0;
    std::size_t size_ = 0;
    bool closed_ = false;
};

} // namespace inferno

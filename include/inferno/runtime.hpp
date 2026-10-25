#pragma once

#include "inferno/bounded_queue.hpp"
#include "inferno/inference_backend.hpp"
#include "inferno/metrics.hpp"
#include "inferno/request_control.hpp"

#include <future>
#include <memory>
#include <mutex>
#include <thread>

namespace inferno {

enum class SubmitStatus { accepted, full, closed, invalid, expired };

struct GenerationResult {
    RequestId id;
    RequestState state;
    std::string text;
    std::string error;
    RequestMetrics metrics;
};

struct Submission {
    SubmitStatus status;
    std::shared_ptr<const RequestLifecycle> lifecycle;
    std::future<GenerationResult> completion;
    std::shared_ptr<RequestControl> control;
};

// Point-in-time scheduler occupancy. Counts are request items, not bytes or token
// work. A dequeued item stays counted as waiting until the worker publishes it as
// executing under the same admission lock used by submit and this snapshot.
struct SchedulerSnapshot {
    std::size_t waiting_requests;
    std::size_t executing_requests;
    std::size_t queue_capacity;
};

class GenerationRuntime {
public:
    virtual ~GenerationRuntime() = default;
    [[nodiscard]] virtual Submission submit(GenerationRequest request, InferenceBackend::TokenCallback token = {}) = 0;
    [[nodiscard]] virtual SchedulerSnapshot scheduler_snapshot() const = 0;
    [[nodiscard]] virtual std::optional<MemorySnapshot> memory_snapshot() const = 0;
    [[nodiscard]] virtual std::optional<ModelCapabilities> model_capabilities() const { return std::nullopt; }
    virtual void shutdown() = 0;
};

// One active request plus at most queue_capacity waiting requests. Callers must
// keep the runtime alive for all API calls. Shutdown drains and may block until
// the synchronous backend returns; it cannot interrupt a stuck backend.
class Runtime final : public GenerationRuntime {
public:
    Runtime(std::unique_ptr<InferenceBackend> backend, std::size_t queue_capacity);
    ~Runtime();
    Runtime(const Runtime&) = delete;
    Runtime& operator=(const Runtime&) = delete;

    // Token callbacks execute on the worker, outside runtime locks. Keep captures
    // alive through completion; don't call shutdown or wait on this runtime here.
    [[nodiscard]] Submission submit(GenerationRequest request, InferenceBackend::TokenCallback token = {}) override;
    [[nodiscard]] SchedulerSnapshot scheduler_snapshot() const override;
    [[nodiscard]] std::optional<MemorySnapshot> memory_snapshot() const override;
    [[nodiscard]] std::optional<ModelCapabilities> model_capabilities() const override;
    void shutdown() override;

private:
    struct WorkItem {
        GenerationRequest request;
        std::shared_ptr<RequestLifecycle> lifecycle;
        std::promise<GenerationResult> completion;
        RequestTimings timings;
        InferenceBackend::TokenCallback token;
        std::shared_ptr<RequestControl> control;
    };

    void work();

    std::unique_ptr<InferenceBackend> backend_;
    BoundedQueue<std::unique_ptr<WorkItem>> queue_;
    mutable std::mutex admission_mutex_;
    std::mutex shutdown_mutex_;
    std::thread worker_;
    std::size_t waiting_requests_ = 0;
    std::size_t executing_requests_ = 0;
};

} // namespace inferno

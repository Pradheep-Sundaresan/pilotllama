#pragma once

#include "inferno/runtime.hpp"

#include <condition_variable>
#include <cstddef>
#include <deque>
#include <memory>
#include <optional>
#include <span>
#include <string>
#include <thread>
#include <vector>

namespace inferno {

using SequenceId = std::size_t;

struct BatchStepEvent {
    SequenceId sequence_id;
    bool decode_started = false;
    bool generated_token = false;
    struct OutputPiece {
        std::string bytes;
        OutputChannel channel = OutputChannel::content;
        std::uint32_t tool_call_index = 0;
        std::string tool_call_id;
        std::string tool_name;
    };
    std::vector<OutputPiece> output_pieces;
    bool finished = false;
    std::string error;
    // Per-turn measured speculative work. Absent when DFlash is not active.
    std::optional<SpeculativeMetrics> speculative_metrics;
};

// All methods are called by one engine thread. admit() prepares only the named
// sequence and returns its exact tokenized prompt size. step() returns exactly one
// event for every supplied sequence, with at most one token per sequence.
class BatchExecutor {
public:
    virtual ~BatchExecutor() = default;
    virtual std::optional<MemorySnapshot> memory_snapshot() const { return std::nullopt; }
    virtual std::optional<ModelCapabilities> model_capabilities() const { return std::nullopt; }
    virtual std::size_t admit(SequenceId sequence_id, const GenerationRequest& request) = 0;
    virtual std::vector<BatchStepEvent> step(std::span<const SequenceId> sequences,
                                             std::size_t prefill_chunk_tokens) = 0;
    virtual bool remove(SequenceId sequence_id) noexcept = 0;
};

struct BatchEngineConfig {
    std::size_t active_sequences = 2;
    std::size_t waiting_capacity = 2;
    std::size_t per_sequence_context_tokens = 2048;
    std::size_t max_output_tokens = 512;
    std::size_t prefill_chunk_tokens = 128;
    std::size_t delivery_capacity = 2;
};

// Explicit model-free engine boundary for continuous batching. The queue-only
// Runtime remains the production default until native Phase 6B verification.
class ContinuousBatchEngine final : public GenerationRuntime {
public:
    ContinuousBatchEngine(std::unique_ptr<BatchExecutor> executor, BatchEngineConfig config = {});
    ~ContinuousBatchEngine();
    ContinuousBatchEngine(const ContinuousBatchEngine&) = delete;
    ContinuousBatchEngine& operator=(const ContinuousBatchEngine&) = delete;

    [[nodiscard]] Submission submit(GenerationRequest request, InferenceBackend::TokenCallback token = {}) override;
    [[nodiscard]] SchedulerSnapshot scheduler_snapshot() const override;
    [[nodiscard]] std::optional<MemorySnapshot> memory_snapshot() const override;
    [[nodiscard]] std::optional<ModelCapabilities> model_capabilities() const override;
    void shutdown() override;

private:
    struct WorkItem;
    struct ActiveSlot;
    struct RetiringSlot;

    void work();
    void notify_delivery();
    bool admit_one(std::unique_ptr<WorkItem> item);
    void retire(std::size_t active_index, RequestState state, std::string error);
    void fail_closed(std::string error);
    void reap_deliveries(bool join_all);

    std::unique_ptr<BatchExecutor> executor_;
    BatchEngineConfig config_;
    mutable std::mutex mutex_;
    std::condition_variable changed_;
    std::mutex shutdown_mutex_;
    std::deque<std::unique_ptr<WorkItem>> waiting_;
    std::vector<std::unique_ptr<ActiveSlot>> active_;
    std::vector<std::unique_ptr<RetiringSlot>> retiring_;
    std::thread worker_;
    bool closed_ = false;
    bool failed_ = false;
    std::size_t executing_requests_ = 0;
    std::string failure_;
};

} // namespace inferno

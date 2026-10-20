#pragma once

#include <cstdint>
#include <optional>
#include <string>
#include <vector>

namespace inferno {

enum class MemorySamplePoint : std::uint8_t {
    backend_ready = 1,
    request_start,
    prefill_complete,
    request_complete_before_cleanup,
    request_complete_after_cleanup,
};

enum class MeasurementStatus : std::uint8_t { measured = 1, unavailable };

struct ProcessMemorySample {
    MemorySamplePoint point;
    MeasurementStatus status;
    std::uint64_t monotonic_ns;
    std::optional<std::uint64_t> resident_bytes;
    std::optional<std::uint64_t> peak_resident_bytes;
    std::optional<std::uint64_t> physical_footprint_bytes;
    std::optional<std::uint64_t> peak_physical_footprint_bytes;
};

struct LlamaBufferAllocation {
    std::string name;
    std::uint64_t model_bytes;
    std::uint64_t context_bytes;
    std::uint64_t compute_bytes;
};

struct MemorySnapshot {
    // At most one latest sample for each MemorySamplePoint.
    std::vector<ProcessMemorySample> process_samples;
    std::optional<std::uint64_t> model_tensor_bytes;
    std::vector<LlamaBufferAllocation> llama_buffers;
    std::optional<std::uint64_t> llama_model_allocated_bytes;
    std::optional<std::uint64_t> llama_context_allocated_bytes;
    std::optional<std::uint64_t> llama_compute_allocated_bytes;
    std::uint32_t configured_context_tokens = 0;
    std::uint32_t configured_batch_tokens = 0;
    std::uint32_t configured_sequences = 0;
    std::int32_t configured_gpu_layers = 0;
};

// Read-only process accounting. On unsupported platforms, returns an unavailable
// sample with all byte fields absent. The timestamp is comparable only in-process.
[[nodiscard]] ProcessMemorySample capture_process_memory(MemorySamplePoint point) noexcept;

} // namespace inferno

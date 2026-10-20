#pragma once

#include "inferno/inference_backend.hpp"
#include "inferno/batch_engine.hpp"
#include <memory>
#include <string>

namespace inferno {
struct LlamaModelConfig {
    std::string path;
    std::uint32_t context_tokens = 4096;
    std::uint32_t batch_tokens = 128;
    std::int32_t threads = 4;
    std::int32_t gpu_layers = 99;
};

// One native backend per process in this milestone; call generate only serially.
[[nodiscard]] std::unique_ptr<InferenceBackend> make_llama_backend(const LlamaModelConfig& config);
[[nodiscard]] std::unique_ptr<BatchExecutor> make_llama_batch_executor(const LlamaModelConfig& config);
} // namespace inferno

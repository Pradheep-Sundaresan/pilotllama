#include "inferno/model_prompt.hpp"

#include <stdexcept>

namespace inferno {
void validate_user_prompt(std::string_view user_prompt) {
    if (user_prompt.size() > max_user_prompt_bytes) {
        throw std::invalid_argument("Prompt exceeds the 64 KiB input limit");
    }
}

ModelPromptMode select_model_prompt_mode(const ModelPromptMetadata& metadata) {
    if (metadata.architecture.empty()) {
        throw std::invalid_argument("GGUF is missing general.architecture metadata");
    }
    if (metadata.has_encoder || !metadata.has_decoder) {
        throw std::invalid_argument("Only decoder-only text-generation GGUF models are supported");
    }
    if (metadata.has_chat_template) {
        return ModelPromptMode::embedded_template;
    }
    if (metadata.architecture == "nemotron_h" && metadata.parameters == 3973556832ULL) {
        return ModelPromptMode::verified_nemotron_fallback;
    }
    throw std::invalid_argument("GGUF has no embedded chat template and no verified fallback");
}

bool supports_verified_continuous_batching(const ModelPromptMetadata& metadata) noexcept {
    return !metadata.has_encoder && metadata.has_decoder && metadata.architecture == "nemotron_h" &&
           metadata.parameters == 3973556832ULL;
}
} // namespace inferno

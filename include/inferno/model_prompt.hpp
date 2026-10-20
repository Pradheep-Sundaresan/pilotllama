#pragma once

#include <cstddef>
#include <cstdint>
#include <string>
#include <string_view>

namespace inferno {
inline constexpr std::size_t max_user_prompt_bytes = 65536;

enum class ModelPromptMode { embedded_template, verified_nemotron_fallback };

struct ModelPromptMetadata {
    std::string architecture;
    std::uint64_t parameters = 0;
    bool has_encoder = false;
    bool has_decoder = false;
    bool has_chat_template = false;
};

void validate_user_prompt(std::string_view user_prompt);
[[nodiscard]] ModelPromptMode select_model_prompt_mode(const ModelPromptMetadata& metadata);
[[nodiscard]] bool supports_verified_continuous_batching(const ModelPromptMetadata& metadata) noexcept;
} // namespace inferno

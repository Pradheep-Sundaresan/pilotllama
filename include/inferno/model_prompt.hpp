#pragma once

#include <cstddef>
#include <cstdint>
#include <string>
#include <string_view>
#include <span>
#include "inferno/request.hpp"

namespace inferno {
inline constexpr std::size_t max_user_prompt_bytes = 1024 * 1024;

enum class ModelPromptMode { embedded_template, verified_nemotron_fallback };

struct ModelPromptMetadata {
    std::string architecture;
    std::uint64_t parameters = 0;
    bool has_encoder = false;
    bool has_decoder = false;
    bool has_chat_template = false;
    std::uint32_t trained_context_tokens = 0;
};

void validate_user_prompt(std::string_view user_prompt);
void validate_chat_messages(std::span<const ChatMessage> messages);
[[nodiscard]] ModelPromptMode select_model_prompt_mode(const ModelPromptMetadata& metadata);
[[nodiscard]] bool qualifies_for_continuous_batching(const ModelPromptMetadata& metadata) noexcept;
[[nodiscard]] bool has_verified_continuous_batching(const ModelPromptMetadata& metadata) noexcept;
} // namespace inferno

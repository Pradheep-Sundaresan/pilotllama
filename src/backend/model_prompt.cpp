#include "inferno/model_prompt.hpp"

#include <stdexcept>
#include <string>
#include <unordered_set>

namespace inferno {
void validate_user_prompt(std::string_view user_prompt) {
    if (user_prompt.size() > max_user_prompt_bytes) {
        throw std::invalid_argument("Prompt exceeds the 1 MiB input limit");
    }
}

void validate_chat_messages(std::span<const ChatMessage> messages) {
    if (messages.empty() || messages.size() > 64) throw std::invalid_argument("Chat requires 1..64 messages");
    std::size_t total = 0;
    enum class Expected { user, assistant, tools, assistant_after_tools } expected = Expected::user;
    std::unordered_set<std::string> pending;
    for (std::size_t i = 0; i < messages.size(); ++i) {
        const auto& message = messages[i];
        if (message.content.find("<|im_start|>") != std::string::npos ||
            message.content.find("<|im_end|>") != std::string::npos ||
            message.content.size() > max_user_prompt_bytes - total) {
            throw std::invalid_argument("Invalid or oversized chat content");
        }
        total += message.content.size();
        if (message.role == ChatRole::system) {
            if (i != 0) throw std::invalid_argument("System message must be first");
            continue;
        }
        if (message.role == ChatRole::user) {
            if (expected != Expected::user || !message.tool_calls.empty() || !message.tool_call_id.empty())
                throw std::invalid_argument("Unexpected user message");
            expected = Expected::assistant;
        } else if (message.role == ChatRole::assistant) {
            if (expected != Expected::assistant && expected != Expected::assistant_after_tools)
                throw std::invalid_argument("Unexpected assistant message");
            if (!message.tool_call_id.empty()) throw std::invalid_argument("Assistant cannot be a tool result");
            pending.clear();
            for (const auto& call : message.tool_calls) {
                if (call.id.empty() || call.id.size() > 128 || call.name.empty() || call.name.size() > 64 ||
                    call.arguments_json.empty() || call.arguments_json.size() > 65536 || !pending.insert(call.id).second)
                    throw std::invalid_argument("Invalid assistant tool call");
            }
            expected = pending.empty() ? Expected::user : Expected::tools;
        } else if (message.role == ChatRole::tool) {
            if (expected != Expected::tools || !message.tool_calls.empty() || message.tool_call_id.empty() ||
                pending.erase(message.tool_call_id) != 1) throw std::invalid_argument("Unexpected tool result");
            if (pending.empty()) expected = Expected::assistant_after_tools;
        } else {
            throw std::invalid_argument("Invalid chat role");
        }
    }
    if (expected != Expected::assistant && expected != Expected::assistant_after_tools)
        throw std::invalid_argument("Chat must end with user input or complete tool results");
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

bool has_verified_continuous_batching(const ModelPromptMetadata& metadata) noexcept {
    return !metadata.has_encoder && metadata.has_decoder && metadata.architecture == "nemotron_h" &&
           metadata.parameters == 3973556832ULL;
}

bool qualifies_for_continuous_batching(const ModelPromptMetadata& metadata) noexcept {
    return !metadata.has_encoder && metadata.has_decoder &&
           (metadata.has_chat_template || has_verified_continuous_batching(metadata));
}
} // namespace inferno

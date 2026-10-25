#pragma once

#include <atomic>
#include <chrono>
#include <cstdint>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

namespace inferno {

using RequestId = std::uint64_t;
using RequestClock = std::chrono::steady_clock;

enum class ChatRole { system, user, assistant, tool };
enum class OutputChannel { content, reasoning, tool_call };

struct ToolCall {
    std::string id;
    std::string name;
    std::string arguments_json;
};

struct ChatMessage {
    ChatRole role;
    std::string content;
    std::vector<ToolCall> tool_calls;
    std::string tool_call_id;
};

struct ToolDefinition {
    std::string name;
    std::string description;
    std::string parameters_json;
};

enum class ToolChoice { none, automatic, required };
enum class ResponseFormat { text, json_object, json_schema };

struct GenerationRequest {
    RequestId id = 0;
    std::string prompt;
    std::uint32_t max_output_tokens = 128;
    std::optional<RequestClock::time_point> deadline;
    // Structured public chat input. Empty preserves the internal legacy prompt path.
    std::vector<ChatMessage> messages;
    // Phase 8D internal contract. Native llama parsing remains fail-closed until 8D-2.
    bool enable_thinking = false;
    // Phase 8E sampling. Zero temperature preserves deterministic greedy decoding.
    float temperature = 0.0F;
    float top_p = 1.0F;
    std::int32_t top_k = 0;
    std::optional<std::uint32_t> seed;
    std::vector<std::string> stop;
    std::vector<ToolDefinition> tools;
    ToolChoice tool_choice = ToolChoice::none;
    ResponseFormat response_format = ResponseFormat::text;
    std::string response_schema_json;
};

struct GeneratedPiece {
    std::string_view bytes;
    OutputChannel channel = OutputChannel::content;
    bool advances_output_tokens = true;
    std::uint32_t tool_call_index = 0;
    std::string_view tool_call_id;
    std::string_view tool_name;

    GeneratedPiece(std::string_view value, OutputChannel kind = OutputChannel::content, bool advances = true)
        : bytes(value), channel(kind), advances_output_tokens(advances) {}
    GeneratedPiece(const std::string& value, OutputChannel kind = OutputChannel::content, bool advances = true)
        : bytes(value), channel(kind), advances_output_tokens(advances) {}
    GeneratedPiece(const char* value, OutputChannel kind = OutputChannel::content, bool advances = true)
        : bytes(value), channel(kind), advances_output_tokens(advances) {}
    GeneratedPiece(std::string_view value, OutputChannel kind, bool advances, std::uint32_t index,
                   std::string_view call_id, std::string_view name)
        : bytes(value), channel(kind), advances_output_tokens(advances), tool_call_index(index),
          tool_call_id(call_id), tool_name(name) {}
};

enum class RequestState {
    created, queued, admitted, prefill, decoding,
    completed, cancelled, failed, timed_out, rejected
};

[[nodiscard]] bool is_terminal(RequestState state) noexcept;
[[nodiscard]] bool can_transition(RequestState from, RequestState to) noexcept;
[[nodiscard]] std::string_view to_string(RequestState state) noexcept;

// State transitions are atomic; synchronization of request data remains the owner's job.
class RequestLifecycle {
public:
    [[nodiscard]] RequestState state() const noexcept;
    [[nodiscard]] bool transition_to(RequestState next) noexcept;

private:
    std::atomic<RequestState> state_{RequestState::created};
};

} // namespace inferno

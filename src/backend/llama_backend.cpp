#include "inferno/llama_backend.hpp"
#include "inferno/model_prompt.hpp"
#include "inferno/nemotron_prompt.hpp"
#include "chat.h"
#include "common.h"
#include "sampling.h"
#include "speculative.h"
#include "llama-ext.h"
#include "llama.h"
#include "ggml-backend.h"

#include <algorithm>
#include <array>
#include <cmath>
#include <cstring>
#include <filesystem>
#include <functional>
#include <limits>
#include <map>
#include <mutex>
#include <stdexcept>
#include <vector>

namespace inferno {
namespace {
constexpr std::size_t max_formatted_prompt_bytes = 1024 * 1024;

struct PromptFormatter {
    ModelPromptMode mode = ModelPromptMode::verified_nemotron_fallback;
    common_chat_templates_ptr embedded_templates;
    bool continuous_batching_verified = false;
    ModelCapabilities capabilities;
};

struct ParsedPiece {
    std::string bytes;
    OutputChannel channel = OutputChannel::content;
    std::uint32_t tool_call_index = 0;
    std::string tool_call_id;
    std::string tool_name;
};

class StopFilter {
public:
    explicit StopFilter(std::vector<std::string> stops = {}) : stops_(std::move(stops)) {
        for (const auto& stop : stops_) max_ = std::max(max_, stop.size());
    }

    std::vector<ParsedPiece> push(ParsedPiece piece, bool& stopped) {
        std::vector<ParsedPiece> result;
        if (stops_.empty()) { result.push_back(std::move(piece)); return result; }
        if (channel_ && *channel_ != piece.channel) flush_into(result);
        channel_ = piece.channel;
        pending_ += piece.bytes;
        std::size_t match = std::string::npos;
        for (const auto& stop : stops_) match = std::min(match, pending_.find(stop));
        if (match != std::string::npos) {
            if (match) result.push_back({pending_.substr(0, match), *channel_});
            pending_.clear();
            stopped = true;
            return result;
        }
        const auto keep = max_ > 0 ? max_ - 1 : 0;
        if (pending_.size() > keep) {
            const auto emit = pending_.size() - keep;
            result.push_back({pending_.substr(0, emit), *channel_});
            pending_.erase(0, emit);
        }
        return result;
    }

    std::vector<ParsedPiece> finish() {
        std::vector<ParsedPiece> result;
        flush_into(result);
        return result;
    }

private:
    void flush_into(std::vector<ParsedPiece>& result) {
        if (!pending_.empty() && channel_) result.push_back({std::move(pending_), *channel_});
        pending_.clear();
    }
    std::vector<std::string> stops_;
    std::optional<OutputChannel> channel_;
    std::string pending_;
    std::size_t max_ = 0;
};

struct PreparedPrompt {
    std::string prompt;
    std::unique_ptr<common_chat_session> session;
    common_chat_msg parsed;
    std::size_t output_bytes = 0;
    std::vector<ToolDefinition> tools;
};

common_chat_templates_inputs template_inputs(std::span<const ChatMessage> messages, bool enable_thinking,
                                             std::span<const ToolDefinition> tools = {},
                                             ToolChoice tool_choice = ToolChoice::none) {
    common_chat_templates_inputs inputs;
    inputs.enable_thinking = enable_thinking;
    for (const auto& tool : tools) inputs.tools.push_back({tool.name, tool.description, tool.parameters_json});
    switch (tool_choice) {
    case ToolChoice::none: inputs.tool_choice = COMMON_CHAT_TOOL_CHOICE_NONE; break;
    case ToolChoice::automatic: inputs.tool_choice = COMMON_CHAT_TOOL_CHOICE_AUTO; break;
    case ToolChoice::required: inputs.tool_choice = COMMON_CHAT_TOOL_CHOICE_REQUIRED; break;
    }
    inputs.reasoning_format = COMMON_REASONING_FORMAT_DEEPSEEK;
    for (const auto& message : messages) {
        inputs.messages.emplace_back();
        switch (message.role) {
        case ChatRole::system: inputs.messages.back().role = "system"; break;
        case ChatRole::user: inputs.messages.back().role = "user"; break;
        case ChatRole::assistant: inputs.messages.back().role = "assistant"; break;
        case ChatRole::tool: inputs.messages.back().role = "tool"; break;
        }
        inputs.messages.back().content = message.content;
        inputs.messages.back().tool_call_id = message.tool_call_id;
        for (const auto& call : message.tool_calls)
            inputs.messages.back().tool_calls.push_back({call.name, call.arguments_json, call.id});
    }
    return inputs;
}

std::string render_embedded_template(const common_chat_templates* templates, std::span<const ChatMessage> messages) {
    auto inputs = template_inputs(messages, false);
    const auto rendered = common_chat_templates_apply(templates, inputs).prompt.str();
    if (rendered.empty()) {
        throw std::invalid_argument("GGUF chat template rendered an empty prompt");
    }
    if (rendered.size() > max_formatted_prompt_bytes) {
        throw std::invalid_argument("Formatted prompt exceeds the 1 MiB safety limit");
    }
    return rendered;
}

std::string render_embedded_template(const common_chat_templates* templates, std::string_view user_prompt) {
    const std::array<ChatMessage, 1> messages{{{ChatRole::user, std::string(user_prompt)}}};
    return render_embedded_template(templates, messages);
}

std::string required_model_metadata(const llama_model* model, const char* key) {
    std::array<char, 256> value{};
    const auto length = llama_model_meta_val_str(model, key, value.data(), value.size());
    if (length < 0) return {};
    if (static_cast<std::size_t>(length) >= value.size()) {
        throw std::invalid_argument(std::string("GGUF metadata is too long: ") + key);
    }
    return std::string(value.data(), static_cast<std::size_t>(length));
}

PromptFormatter inspect_prompt_formatter(const llama_model* model) {
    const char* raw_template = llama_model_chat_template(model, nullptr);
    ModelPromptMetadata metadata{
        required_model_metadata(model, "general.architecture"),
        llama_model_n_params(model),
        llama_model_has_encoder(model),
        llama_model_has_decoder(model),
        raw_template != nullptr && raw_template[0] != '\0',
        static_cast<std::uint32_t>(std::max(0, llama_model_n_ctx_train(model)))
    };
    PromptFormatter result;
    result.mode = select_model_prompt_mode(metadata);
    result.continuous_batching_verified = has_verified_continuous_batching(metadata);
    result.capabilities.architecture = metadata.architecture;
    result.capabilities.parameter_count = metadata.parameters;
    result.capabilities.trained_context_tokens = metadata.trained_context_tokens;
    result.capabilities.has_encoder = metadata.has_encoder;
    result.capabilities.has_decoder = metadata.has_decoder;
    result.capabilities.has_embedded_chat_template = metadata.has_chat_template;
    result.capabilities.continuous_mode = has_verified_continuous_batching(metadata)
        ? CapabilitySupport::verified
        : (qualifies_for_continuous_batching(metadata) ? CapabilitySupport::detected : CapabilitySupport::unavailable);
    if (result.mode == ModelPromptMode::embedded_template) {
        if (std::strlen(raw_template) > max_formatted_prompt_bytes) {
            throw std::invalid_argument("GGUF chat template exceeds the 1 MiB safety limit");
        }
        result.embedded_templates = common_chat_templates_init(model, "");
        if (!result.embedded_templates) throw std::invalid_argument("Could not initialize the GGUF chat template");
        (void) render_embedded_template(result.embedded_templates.get(), "template compatibility check");
        result.capabilities.compatibility = CompatibilityLevel::compatible;
        result.capabilities.template_source = PromptTemplateSource::embedded;
        result.capabilities.queue_mode = CapabilitySupport::detected;

        auto probe = [&](common_chat_templates_inputs inputs, std::initializer_list<std::string_view> markers) {
            try {
                const auto rendered = common_chat_templates_apply(result.embedded_templates.get(), inputs);
                const auto text = rendered.prompt.str();
                for (const auto marker : markers) if (text.find(marker) == std::string::npos) return false;
                return true;
            } catch (...) {
                return false;
            }
        };
        common_chat_templates_inputs conversation;
        conversation.enable_thinking = false;
        conversation.messages = {{.role = "system", .content = "pilot_system_probe"},
                                 {.role = "user", .content = "pilot_user_probe_one"},
                                 {.role = "assistant", .content = "pilot_assistant_probe"},
                                 {.role = "user", .content = "pilot_user_probe_two"}};
        const bool conversation_supported = probe(conversation,
            {"pilot_system_probe", "pilot_user_probe_one", "pilot_assistant_probe", "pilot_user_probe_two"});
        result.capabilities.system_messages = conversation_supported
            ? CapabilitySupport::detected : CapabilitySupport::unavailable;
        result.capabilities.multi_turn = conversation_supported
            ? CapabilitySupport::detected : CapabilitySupport::unavailable;

        common_chat_templates_inputs thinking;
        thinking.enable_thinking = true;
        thinking.messages = {{.role = "user", .content = "pilot_thinking_probe"}};
        try {
            const auto rendered = common_chat_templates_apply(result.embedded_templates.get(), thinking);
            result.capabilities.thinking = rendered.supports_thinking
                ? CapabilitySupport::detected : CapabilitySupport::unavailable;
        } catch (...) {
            result.capabilities.thinking = CapabilitySupport::unavailable;
        }

        common_chat_templates_inputs tools;
        tools.enable_thinking = false;
        tools.messages = {{.role = "user", .content = "pilot_tool_probe"}};
        tools.tools = {{"pilot_probe_tool", "Capability probe", R"({"type":"object","properties":{}})"}};
        result.capabilities.tools = probe(tools, {"pilot_probe_tool"})
            ? CapabilitySupport::detected : CapabilitySupport::unavailable;
    } else {
        result.capabilities.compatibility = CompatibilityLevel::verified;
        result.capabilities.template_source = PromptTemplateSource::verified_fallback;
        result.capabilities.queue_mode = CapabilitySupport::verified;
        result.capabilities.system_messages = CapabilitySupport::unavailable;
        result.capabilities.multi_turn = CapabilitySupport::unavailable;
        result.capabilities.thinking = CapabilitySupport::unavailable;
        result.capabilities.tools = CapabilitySupport::unavailable;
    }
    return result;
}

std::string format_model_prompt(const PromptFormatter& formatter, const GenerationRequest& request) {
    std::vector<ChatMessage> single;
    auto messages = std::span<const ChatMessage>(request.messages);
    if (messages.empty()) {
        validate_user_prompt(request.prompt);
        single.push_back({ChatRole::user, request.prompt});
        messages = std::span<const ChatMessage>(single);
    } else {
        if (!request.prompt.empty()) throw std::invalid_argument("Use prompt or messages, not both");
        validate_chat_messages(messages);
    }
    if (formatter.mode == ModelPromptMode::verified_nemotron_fallback) {
        if (messages.size() != 1 || messages.front().role != ChatRole::user) {
            throw std::invalid_argument("This verified fallback supports one user message only");
        }
        return format_nemotron_prompt(messages.front().content);
    }
    const bool has_system = std::any_of(messages.begin(), messages.end(),
        [](const auto& message) { return message.role == ChatRole::system; });
    if (has_system && formatter.capabilities.system_messages == CapabilitySupport::unavailable) {
        throw std::invalid_argument("Loaded template does not support system messages");
    }
    if (messages.size() > (has_system ? 2U : 1U) &&
        formatter.capabilities.multi_turn == CapabilitySupport::unavailable) {
        throw std::invalid_argument("Loaded template does not support multi-turn chat");
    }
    return render_embedded_template(formatter.embedded_templates.get(), messages);
}

PreparedPrompt prepare_model_prompt(const PromptFormatter& formatter, const GenerationRequest& request,
                                    const llama_vocab* vocab) {
    PreparedPrompt result;
    if (request.enable_thinking && formatter.capabilities.thinking == CapabilitySupport::unavailable) {
        throw std::invalid_argument("Loaded model template does not support thinking output");
    }
    if (!request.tools.empty() && formatter.capabilities.tools == CapabilitySupport::unavailable) {
        throw std::invalid_argument("Loaded model template does not support tools");
    }
    if (request.response_format != ResponseFormat::text && formatter.mode != ModelPromptMode::embedded_template)
        throw std::invalid_argument("Structured output requires an embedded template");
    if (request.tools.empty() && request.tool_choice != ToolChoice::none) {
        throw std::invalid_argument("Invalid tools and tool choice combination");
    }
    if (formatter.mode == ModelPromptMode::verified_nemotron_fallback) {
        result.prompt = format_model_prompt(formatter, request);
        return result;
    }
    std::vector<ChatMessage> single;
    auto messages = std::span<const ChatMessage>(request.messages);
    if (messages.empty()) {
        validate_user_prompt(request.prompt);
        single.push_back({ChatRole::user, request.prompt});
        messages = single;
    } else {
        if (!request.prompt.empty()) throw std::invalid_argument("Use prompt or messages, not both");
        validate_chat_messages(messages);
    }
    // Reuse the same capability checks as ordinary formatting before constructing
    // llama.cpp's incremental semantic parser.
    (void) format_model_prompt(formatter, request);
    auto inputs = template_inputs(messages, request.enable_thinking, request.tools, request.tool_choice);
    if (request.response_format != ResponseFormat::text) inputs.json_schema = request.response_schema_json;
    result.session = std::make_unique<common_chat_session>(formatter.embedded_templates.get(), vocab, inputs);
    result.prompt = result.session->prompt().str();
    if (result.prompt.empty()) throw std::invalid_argument("GGUF chat template rendered an empty prompt");
    if (result.prompt.size() > max_formatted_prompt_bytes) {
        throw std::invalid_argument("Formatted prompt exceeds the 1 MiB safety limit");
    }
    result.parsed = result.session->msg();
    result.tools = request.tools;
    return result;
}

std::vector<ParsedPiece> parsed_deltas(PreparedPrompt& prepared, const common_chat_msg& current, bool final = false) {
    std::vector<ParsedPiece> result;
    if (current.tool_calls.size() > 16) throw std::length_error("Too many parsed tool calls");
    std::size_t tool_bytes = 0;
    for (const auto& call : current.tool_calls) {
        if (call.id.size() > 128 || call.name.size() > 64 || call.arguments.size() > 65536 ||
            tool_bytes > 262144 - call.arguments.size()) throw std::length_error("Parsed tool call exceeds bounds");
        tool_bytes += call.arguments.size();
        const auto name_match = std::find_if(prepared.tools.begin(), prepared.tools.end(), [&](const auto& tool) {
            return final ? tool.name == call.name : tool.name.starts_with(call.name);
        });
        if (call.name.empty() ? final : name_match == prepared.tools.end())
            throw std::invalid_argument("Model emitted an unknown tool name");
        if (final) {
            const auto arguments = common_json::parse(call.arguments);
            if (!arguments.is_object()) throw std::invalid_argument("Tool arguments must be a JSON object");
        }
    }
    for (const auto& diff : common_chat_msg_diff::compute_diffs(prepared.parsed, current)) {
        if (diff.tool_call_index != std::string::npos) {
            if (diff.tool_call_index > std::numeric_limits<std::uint32_t>::max())
                throw std::length_error("Tool call index overflow");
            result.push_back({diff.tool_call_delta.arguments, OutputChannel::tool_call,
                static_cast<std::uint32_t>(diff.tool_call_index), diff.tool_call_delta.id, diff.tool_call_delta.name});
        }
        if (!diff.reasoning_content_delta.empty()) {
            result.push_back({diff.reasoning_content_delta, OutputChannel::reasoning});
        }
        if (!diff.content_delta.empty()) result.push_back({diff.content_delta, OutputChannel::content});
    }
    prepared.parsed = current;
    for (const auto& piece : result) {
        if (piece.bytes.size() > max_formatted_prompt_bytes - prepared.output_bytes) {
            throw std::length_error("Parsed model output exceeds the 1 MiB safety limit");
        }
        prepared.output_bytes += piece.bytes.size();
    }
    return result;
}

std::vector<ParsedPiece> feed_parser(PreparedPrompt& prepared, std::string piece, llama_token token) {
    if (!prepared.session) return {{std::move(piece), OutputChannel::content}};
    common_chat_input input;
    input.append(piece, token);
    return parsed_deltas(prepared, prepared.session->feed(input));
}

std::vector<ParsedPiece> finish_parser(PreparedPrompt& prepared) {
    if (!prepared.session) return {};
    return parsed_deltas(prepared, prepared.session->finish(), true);
}

struct BackendSession {
    BackendSession() { common_init(); llama_backend_init(); }
    ~BackendSession() { llama_backend_free(); }
};

template <typename T, void (*Free)(T*)>
using Owned = std::unique_ptr<T, decltype(Free)>;

Owned<llama_sampler, llama_sampler_free> make_sampler(const GenerationRequest& request, const llama_vocab* vocab,
                                                       std::string_view grammar = {}) {
    if (request.temperature == 0.0F) {
        if (grammar.empty()) return {llama_sampler_init_greedy(), llama_sampler_free};
    }
    auto* chain = llama_sampler_chain_init(llama_sampler_chain_default_params());
    if (!chain) return {nullptr, llama_sampler_free};
    if (!grammar.empty()) llama_sampler_chain_add(chain, llama_sampler_init_grammar(vocab, std::string(grammar).c_str(), "root"));
    if (request.temperature == 0.0F) {
        llama_sampler_chain_add(chain, llama_sampler_init_greedy());
    } else {
        llama_sampler_chain_add(chain, llama_sampler_init_top_k(request.top_k));
        llama_sampler_chain_add(chain, llama_sampler_init_top_p(request.top_p, 1));
        llama_sampler_chain_add(chain, llama_sampler_init_temp(request.temperature));
        llama_sampler_chain_add(chain, llama_sampler_init_dist(request.seed.value_or(LLAMA_DEFAULT_SEED)));
    }
    return {chain, llama_sampler_free};
}

std::string token_piece(const llama_vocab* vocab, llama_token token) {
    std::array<char, 256> buffer{};
    const auto bytes = llama_token_to_piece(vocab, token, buffer.data(), buffer.size(), 0, false);
    if (bytes == std::numeric_limits<int32_t>::min()) throw std::runtime_error("Token piece length overflow");
    if (bytes < 0) {
        std::string piece(static_cast<std::size_t>(-bytes), '\0');
        if (llama_token_to_piece(vocab, token, piece.data(), -bytes, 0, false) != -bytes)
            throw std::runtime_error("Token conversion failed");
        return piece;
    }
    return std::string(buffer.data(), static_cast<std::size_t>(bytes));
}

struct ClearState {
    llama_context* context;
    std::function<void(MemorySamplePoint)> record;
    ~ClearState() noexcept {
        try { record(MemorySamplePoint::request_complete_before_cleanup); } catch (...) {}
        llama_synchronize(context);
        llama_memory_clear(llama_get_memory(context), true);
        try { record(MemorySamplePoint::request_complete_after_cleanup); } catch (...) {}
    }
};

class LlamaBackend final : public InferenceBackend {
public:
    explicit LlamaBackend(const LlamaModelConfig& config) : config_(config) {
        auto model_params = llama_model_default_params();
        model_params.n_gpu_layers = config.gpu_layers;
        model_.reset(llama_model_load_from_file(config.path.c_str(), model_params));
        if (!model_) {
            throw std::runtime_error("llama.cpp could not load the local GGUF");
        }
        prompt_formatter_ = inspect_prompt_formatter(model_.get());
        if (config.context_tokens > prompt_formatter_.capabilities.trained_context_tokens) {
            throw std::invalid_argument("Configured context exceeds the model's declared trained context");
        }
        prompt_formatter_.capabilities.configured_context_tokens = config.context_tokens;
        prompt_formatter_.capabilities.configured_max_output_tokens = config.max_output_tokens;
        auto params = llama_context_default_params();
        params.n_ctx = config.context_tokens;
        params.n_batch = config.batch_tokens;
        params.n_ubatch = config.batch_tokens;
        params.n_threads = config.threads;
        params.n_threads_batch = config.threads;
        params.n_seq_max = 1;
        if (!config.draft_model_path.empty()) {
            params.n_rs_seq = config.draft_tokens;
            params.n_outputs_max = config.draft_tokens + 1;
            params.n_outputs_max_per_seq = config.draft_tokens + 1;
        }
        context_.reset(llama_init_from_model(model_.get(), params));
        if (!context_) {
            throw std::runtime_error("llama.cpp could not create the execution context");
        }
        if (!config.draft_model_path.empty()) {
            common_params draft;
            draft.model.path = config.path;
            draft.n_ctx = config.context_tokens;
            draft.n_batch = std::max(config.batch_tokens, config.draft_tokens + 1);
            draft.n_ubatch = draft.n_batch;
            draft.n_parallel = 1;
            draft.n_gpu_layers = config.gpu_layers;
            draft.cpuparams.n_threads = config.threads;
            draft.cpuparams_batch.n_threads = config.threads;
            draft.speculative.types = {COMMON_SPECULATIVE_TYPE_DRAFT_DFLASH};
            draft.speculative.draft.mparams.path = config.draft_model_path;
            draft.speculative.draft.n_max = static_cast<std::int32_t>(config.draft_tokens);
            draft.speculative.draft.n_gpu_layers = config.gpu_layers;
            auto draft_params = common_base_params_to_speculative(draft);
            speculative_init_ = common_speculative_init_from_params(draft_params, model_.get(), context_.get());
            if (!speculative_init_ || !speculative_init_->model() || !speculative_init_->context()) {
                throw std::runtime_error("llama.cpp could not initialize the DFlash draft model");
            }
            draft.speculative.draft.ctx_tgt = context_.get();
            draft.speculative.draft.ctx_dft = speculative_init_->context();
            speculative_params_ = std::move(draft.speculative);
        }
        initialize_memory_snapshot();
        record_process_memory(MemorySamplePoint::backend_ready);
    }

    std::optional<MemorySnapshot> memory_snapshot() const override {
        std::lock_guard lock(memory_mutex_);
        auto result = memory_snapshot_;
        result.process_samples.clear();
        for (const auto& sample : process_samples_) {
            if (sample) result.process_samples.push_back(*sample);
        }
        return result;
    }

    std::optional<ModelCapabilities> model_capabilities() const override {
        return prompt_formatter_.capabilities;
    }

    std::optional<SpeculativeMetrics> take_speculative_metrics() override {
        std::lock_guard lock(memory_mutex_);
        auto result = speculative_metrics_;
        speculative_metrics_.reset();
        return result;
    }

    void generate(const GenerationRequest& request, const DecodeStarted& decode_started,
                  const TokenCallback& emit_token, const Checkpoint& checkpoint) override {
        record_process_memory(MemorySamplePoint::request_start);
        checkpoint();
        if (request.max_output_tokens == 0 || request.max_output_tokens > config_.max_output_tokens) {
            throw std::invalid_argument("Requested output exceeds the configured runtime budget");
        }
        const auto* vocab = llama_model_get_vocab(model_.get());
        auto prepared = prepare_model_prompt(prompt_formatter_, request, vocab);
        const auto& prompt = prepared.prompt;
        const auto needed = llama_tokenize(vocab, prompt.data(), static_cast<int32_t>(prompt.size()),
                                           nullptr, 0, true, true);
        if (needed >= 0 || needed == std::numeric_limits<int32_t>::min()) {
            throw std::runtime_error("Could not determine prompt token count");
        }
        const auto count = static_cast<std::uint32_t>(-needed);
        if (static_cast<std::uint64_t>(count) + request.max_output_tokens > config_.context_tokens) {
            throw std::invalid_argument("Prompt plus output budget exceeds configured context");
        }
        std::vector<llama_token> tokens(count);
        if (llama_tokenize(vocab, prompt.data(), static_cast<int32_t>(prompt.size()), tokens.data(),
                           static_cast<int32_t>(tokens.size()), true, true) != static_cast<int32_t>(count)) {
            throw std::runtime_error("Prompt tokenization failed");
        }
		const auto grammar = prepared.session && (request.response_format != ResponseFormat::text || request.tool_choice == ToolChoice::required) ? prepared.session->grammar() : std::string_view{};
		if (!config_.draft_model_path.empty()) {
            generate_speculative(request, prepared, tokens, grammar, decode_started, emit_token, checkpoint);
            return;
        }
		ClearState reset{context_.get(), [this](MemorySamplePoint point) { record_process_memory(point); }};
		auto sampler = make_sampler(request, vocab, grammar);
        Owned<llama_batch_ext, llama_batch_ext_free> batch(llama_batch_ext_init(context_.get()), llama_batch_ext_free);
        if (!sampler || !batch) {
            throw std::runtime_error("Could not allocate sampler or batch");
        }
        auto add = [&](llama_token token, llama_pos position, bool logits) {
            const auto index = llama_batch_ext_add_token(batch.get(), 0, token);
            if (index < 0 || !llama_batch_ext_set_pos(batch.get(), index, &position) ||
                !llama_batch_ext_set_output_logits(batch.get(), index, logits)) {
                throw std::runtime_error("Could not construct inference batch");
            }
        };
        auto evaluate = [&] {
            checkpoint();
            const auto code = llama_process(context_.get(), LLAMA_PROCESS_TYPE_DECODE, batch.get());
            if (code != 0) {
                throw std::runtime_error("llama.cpp evaluation failed with code " + std::to_string(code));
            }
            checkpoint();
        };
        for (std::uint32_t offset = 0; offset < count;) {
            llama_batch_ext_clear(batch.get());
            const auto end = std::min(count, offset + config_.batch_tokens);
            for (auto i = offset; i < end; ++i) {
                add(tokens[i], static_cast<llama_pos>(i), i + 1 == end);
            }
            evaluate();
            offset = end;
        }
        llama_synchronize(context_.get());
        record_process_memory(MemorySamplePoint::prefill_complete);
        decode_started();
        StopFilter stop_filter(request.stop);
        bool stopped = false;
        for (std::uint32_t generated = 0; generated < request.max_output_tokens; ++generated) {
            checkpoint();
            const auto token = llama_sampler_sample(sampler.get(), context_.get(), -1);
            if (llama_vocab_is_eog(vocab, token)) {
                break;
            }
            emit_token({std::string{}, OutputChannel::content, true});
            for (auto& output : feed_parser(prepared, token_piece(vocab, token), token)) {
                if (output.channel == OutputChannel::tool_call) {
                    for (auto& filtered : stop_filter.finish()) emit_token({filtered.bytes, filtered.channel, false});
                    emit_token({output.bytes, output.channel, false, output.tool_call_index, output.tool_call_id, output.tool_name});
                } else for (auto& filtered : stop_filter.push(std::move(output), stopped))
                    emit_token({filtered.bytes, filtered.channel, false});
                if (stopped) break;
            }
            if (stopped) break;
            if (generated + 1 < request.max_output_tokens) {
                llama_batch_ext_clear(batch.get());
                add(token, static_cast<llama_pos>(count + generated), true);
                evaluate();
            }
        }
        if (!stopped) {
            for (auto& output : finish_parser(prepared)) {
                if (output.channel == OutputChannel::tool_call) {
                    for (auto& filtered : stop_filter.finish()) emit_token({filtered.bytes, filtered.channel, false});
                    emit_token({output.bytes, output.channel, false, output.tool_call_index, output.tool_call_id, output.tool_name});
                } else for (auto& filtered : stop_filter.push(std::move(output), stopped))
                    emit_token({filtered.bytes, filtered.channel, false});
                if (stopped) break;
            }
            if (!stopped) for (auto& output : stop_filter.finish())
                emit_token({output.bytes, output.channel, false});
        }
    }

private:
    void generate_speculative(const GenerationRequest& request, PreparedPrompt& prepared,
                              const std::vector<llama_token>& tokens, std::string_view grammar,
                              const DecodeStarted& decode_started, const TokenCallback& emit_token,
                              const Checkpoint& checkpoint) {
        if (tokens.empty()) throw std::runtime_error("DFlash requires a non-empty prompt");
        {
            std::lock_guard lock(memory_mutex_);
            speculative_metrics_ = SpeculativeMetrics{};
        }
        const auto* vocab = llama_model_get_vocab(model_.get());
        common_params_sampling sampling;
        sampling.seed = request.seed.value_or(LLAMA_DEFAULT_SEED);
        sampling.top_k = request.top_k;
        sampling.top_p = request.top_p;
        sampling.min_p = 0.0F;
        sampling.temp = request.temperature;
        if (!grammar.empty()) sampling.grammar = {COMMON_GRAMMAR_TYPE_USER, std::string(grammar)};
        common_sampler_ptr sampler(common_sampler_init(model_.get(), sampling));
        common_speculative_ptr spec(common_speculative_init(speculative_params_, 1));
        if (!sampler || !spec) throw std::runtime_error("Could not initialize DFlash generation");

        auto* draft_context = speculative_init_->context();
        struct ResetBoth {
            llama_context* target;
            llama_context* draft;
            std::function<void(MemorySamplePoint)> record;
            ~ResetBoth() noexcept {
                try { record(MemorySamplePoint::request_complete_before_cleanup); } catch (...) {}
                llama_synchronize(target);
                llama_synchronize(draft);
                llama_memory_clear(llama_get_memory(target), true);
                llama_memory_clear(llama_get_memory(draft), true);
                try { record(MemorySamplePoint::request_complete_after_cleanup); } catch (...) {}
            }
        } reset{context_.get(), draft_context,
                [this](MemorySamplePoint point) { record_process_memory(point); }};

        constexpr llama_seq_id sequence = 0;
        // Capability probing clears context memory; it must happen before prompt evaluation.
        const bool checkpoint_target = common_context_can_seq_rm(context_.get()) == COMMON_CONTEXT_SEQ_RM_TYPE_FULL;
        const bool checkpoint_draft = common_context_can_seq_rm(draft_context) == COMMON_CONTEXT_SEQ_RM_TYPE_FULL;
        llama_tokens prompt(tokens.begin(), tokens.end() - 1);
        prompt.reserve(config_.context_tokens);
        for (std::size_t offset = 0; offset < prompt.size();) {
            checkpoint();
            common_batch batch(context_.get());
            const auto end = std::min(prompt.size(), offset + config_.batch_tokens);
            for (auto i = offset; i < end; ++i) batch.add(prompt[i], static_cast<llama_pos>(i), sequence, false);
            if (llama_process(context_.get(), LLAMA_PROCESS_TYPE_DECODE, batch.get()) != 0 ||
                !common_speculative_process(spec.get(), batch)) {
                throw std::runtime_error("DFlash prompt evaluation failed");
            }
            offset = end;
        }
        llama_token last = tokens.back();
        int past = static_cast<int>(prompt.size());
        common_speculative_begin(spec.get(), sequence, prompt);
        common_batch target_batch(context_.get());
        common_prompt_checkpoint saved;
        llama_tokens draft;
        StopFilter stop_filter(request.stop);
        bool stopped = false;
        bool eos = false;
        std::uint32_t generated = 0;
        decode_started();
        record_process_memory(MemorySamplePoint::prefill_complete);

        while (generated < request.max_output_tokens && !stopped && !eos) {
            checkpoint();
            bool drafted_now = false;
            if (draft.empty()) {
                saved.update_pos(prompt.size(), llama_memory_seq_pos_min(llama_get_memory(context_.get()), sequence),
                                 llama_memory_seq_pos_max(llama_get_memory(context_.get()), sequence));
                if (checkpoint_draft) saved.update_dft(draft_context, sequence, LLAMA_STATE_SEQ_FLAGS_PARTIAL_ONLY);
                const auto remaining = request.max_output_tokens - generated;
                const auto room = config_.context_tokens - static_cast<std::uint32_t>(past) - 1;
                const auto maximum = std::min({config_.draft_tokens, remaining > 0 ? remaining - 1 : 0U, room});
                common_speculative_get_draft_params(spec.get(), sequence) = {
                    .drafting = true, .n_max = static_cast<std::int32_t>(maximum), .pos0 = past,
                    .id_last = last, .prompt = &prompt, .result = &draft,
                    .temp = request.temperature, .seed = sampling.seed};
                common_speculative_draft(spec.get());
                drafted_now = true;
                if (!draft.empty() && checkpoint_target)
                    saved.update_tgt(context_.get(), sequence, LLAMA_STATE_SEQ_FLAGS_PARTIAL_ONLY);
                if (checkpoint_draft) {
                    if (!saved.load_dft(draft_context, sequence, LLAMA_STATE_SEQ_FLAGS_PARTIAL_ONLY))
                        throw std::runtime_error("Could not restore DFlash checkpoint");
                }
                llama_memory_seq_rm(llama_get_memory(draft_context), sequence, saved.pos_max + 1, -1);
            }

            target_batch.clear();
            target_batch.add(last, past++, sequence, true);
            for (std::size_t i = 0; i < draft.size(); ++i)
                target_batch.add(draft[i], past + static_cast<llama_pos>(i), sequence, true);
            if (llama_process(context_.get(), LLAMA_PROCESS_TYPE_DECODE, target_batch.get()) != 0 ||
                !common_speculative_process(spec.get(), target_batch))
                throw std::runtime_error("DFlash verification failed");
            auto sampler_saved = checkpoint_target ? common_sampler_ptr(common_sampler_clone(sampler.get())) : nullptr;
            const auto draft_count = draft.size();
            auto accepted = common_sampler_sample_and_accept_n(sampler.get(), context_.get(), draft);
            if (drafted_now) {
                std::lock_guard lock(memory_mutex_);
                speculative_metrics_->drafted_tokens += draft_count;
                speculative_metrics_->accepted_tokens += accepted.size() - 1;
            }
            if (checkpoint_target && accepted.size() - 1 < draft_count) {
                draft = std::move(accepted);
                if (!saved.load_tgt(context_.get(), sequence, LLAMA_STATE_SEQ_FLAGS_PARTIAL_ONLY) ||
                    !saved.load_dft(draft_context, sequence, LLAMA_STATE_SEQ_FLAGS_PARTIAL_ONLY))
                    throw std::runtime_error("Could not restore speculative checkpoint");
                llama_memory_seq_rm(llama_get_memory(context_.get()), sequence, saved.pos_max + 1, -1);
                llama_memory_seq_rm(llama_get_memory(draft_context), sequence, saved.pos_max + 1, -1);
                prompt.resize(saved.n_tokens);
                sampler = std::move(sampler_saved);
                past = static_cast<int>(prompt.size());
                continue;
            }
            common_speculative_accept(spec.get(), sequence, static_cast<std::uint16_t>(accepted.size() - 1));
            past += static_cast<int>(accepted.size() - 1);
            for (const auto token : accepted) {
                prompt.push_back(last);
                last = token;
                if (llama_vocab_is_eog(vocab, token)) { eos = true; break; }
                emit_token({std::string{}, OutputChannel::content, true});
                for (auto& output : feed_parser(prepared, token_piece(vocab, token), token)) {
                    if (output.channel == OutputChannel::tool_call) {
                        for (auto& filtered : stop_filter.finish()) emit_token({filtered.bytes, filtered.channel, false});
                        emit_token({output.bytes, output.channel, false, output.tool_call_index, output.tool_call_id, output.tool_name});
                    } else for (auto& filtered : stop_filter.push(std::move(output), stopped))
                        emit_token({filtered.bytes, filtered.channel, false});
                    if (stopped) break;
                }
                if (++generated >= request.max_output_tokens || stopped) break;
                checkpoint();
            }
            draft.clear();
            llama_memory_seq_rm(llama_get_memory(context_.get()), sequence, past, -1);
            llama_memory_seq_rm(llama_get_memory(draft_context), sequence, past, -1);
        }
        if (!stopped) {
            for (auto& output : finish_parser(prepared)) {
                if (output.channel == OutputChannel::tool_call) {
                    for (auto& filtered : stop_filter.finish()) emit_token({filtered.bytes, filtered.channel, false});
                    emit_token({output.bytes, output.channel, false, output.tool_call_index, output.tool_call_id, output.tool_name});
                } else for (auto& filtered : stop_filter.push(std::move(output), stopped))
                    emit_token({filtered.bytes, filtered.channel, false});
                if (stopped) break;
            }
            if (!stopped) for (auto& output : stop_filter.finish()) emit_token({output.bytes, output.channel, false});
        }
    }

    static std::size_t sample_index(MemorySamplePoint point) {
        return static_cast<std::size_t>(point) - 1;
    }

    void record_process_memory(MemorySamplePoint point) noexcept {
        const auto sample = capture_process_memory(point);
        std::lock_guard lock(memory_mutex_);
        process_samples_[sample_index(point)] = sample;
    }

    void initialize_memory_snapshot() {
        memory_snapshot_.configured_context_tokens = config_.context_tokens;
        memory_snapshot_.configured_batch_tokens = config_.batch_tokens;
        memory_snapshot_.configured_sequences = 1;
        memory_snapshot_.configured_gpu_layers = config_.gpu_layers;
        std::uint64_t tensor_bytes = llama_model_size(model_.get());
        if (speculative_init_ && speculative_init_->model()) {
            const auto draft_bytes = llama_model_size(speculative_init_->model());
            if (draft_bytes <= std::numeric_limits<std::uint64_t>::max() - tensor_bytes) tensor_bytes += draft_bytes;
        }
        memory_snapshot_.model_tensor_bytes = tensor_bytes;
        const auto breakdown = llama_get_memory_breakdown(context_.get());
        const auto draft_breakdown = speculative_init_ && speculative_init_->context()
            ? llama_get_memory_breakdown(speculative_init_->context()) : decltype(breakdown){};
        constexpr std::size_t row_limit = 32;
        if (breakdown.size() + draft_breakdown.size() > row_limit) return;
        std::uint64_t model_total = 0, context_total = 0, compute_total = 0;
        auto add = [](std::uint64_t& total, std::size_t value) {
            if (value > std::numeric_limits<std::uint64_t>::max() - total) return false;
            total += static_cast<std::uint64_t>(value);
            return true;
        };
        auto append = [&](const auto& rows, std::string_view prefix) {
            for (const auto& [type, bytes] : rows) {
                const char* raw_name = ggml_backend_buft_name(type);
                std::string name(prefix);
                name += raw_name ? raw_name : "unknown";
                if (name.size() > 128) name.resize(128);
                if (!add(model_total, bytes.model) || !add(context_total, bytes.context) ||
                    !add(compute_total, bytes.compute)) return false;
                memory_snapshot_.llama_buffers.push_back({std::move(name), bytes.model, bytes.context, bytes.compute});
            }
            return true;
        };
        if (!append(breakdown, "") || !append(draft_breakdown, "draft:")) {
            memory_snapshot_.llama_buffers.clear();
            return;
        }
        memory_snapshot_.llama_model_allocated_bytes = model_total;
        memory_snapshot_.llama_context_allocated_bytes = context_total;
        memory_snapshot_.llama_compute_allocated_bytes = compute_total;
    }

    LlamaModelConfig config_;
    common_params_speculative speculative_params_;
    common_speculative_init_result_ptr speculative_init_;
    BackendSession session_;
    Owned<llama_model, llama_model_free> model_{nullptr, llama_model_free};
    Owned<llama_context, llama_free> context_{nullptr, llama_free};
    PromptFormatter prompt_formatter_;
    mutable std::mutex memory_mutex_;
    MemorySnapshot memory_snapshot_;
    std::array<std::optional<ProcessMemorySample>, 5> process_samples_;
    std::optional<SpeculativeMetrics> speculative_metrics_;
};

class LlamaBatchExecutor final : public BatchExecutor {
public:
    explicit LlamaBatchExecutor(const LlamaModelConfig& config) : config_(config) {
        auto model_params = llama_model_default_params();
        model_params.n_gpu_layers = config.gpu_layers;
        model_.reset(llama_model_load_from_file(config.path.c_str(), model_params));
        if (!model_) throw std::runtime_error("llama.cpp could not load the local GGUF");
        prompt_formatter_ = inspect_prompt_formatter(model_.get());
        if (config.context_tokens > prompt_formatter_.capabilities.trained_context_tokens) {
            throw std::invalid_argument("Configured context exceeds the model's declared trained context");
        }
        prompt_formatter_.capabilities.configured_context_tokens = config.context_tokens;
        prompt_formatter_.capabilities.configured_max_output_tokens = config.max_output_tokens;
        if (!prompt_formatter_.continuous_batching_verified &&
            !(config.allow_unqualified_continuous &&
              qualifies_for_continuous_batching({prompt_formatter_.capabilities.architecture,
                  prompt_formatter_.capabilities.parameter_count, prompt_formatter_.capabilities.has_encoder,
                  prompt_formatter_.capabilities.has_decoder, prompt_formatter_.capabilities.has_embedded_chat_template,
                  prompt_formatter_.capabilities.trained_context_tokens}))) {
            throw std::invalid_argument("Continuous batching is not qualified for this model");
        }
        auto params = llama_context_default_params();
        params.n_ctx = config.context_tokens;
        params.n_batch = config.batch_tokens;
        params.n_ubatch = config.batch_tokens;
        params.n_threads = config.threads;
        params.n_threads_batch = config.threads;
        params.n_seq_max = 2;
        if (!config.draft_model_path.empty()) {
            params.n_rs_seq = config.draft_tokens;
            params.n_outputs_max = 2 * (config.draft_tokens + 1);
            params.n_outputs_max_per_seq = config.draft_tokens + 1;
        }
        context_.reset(llama_init_from_model(model_.get(), params));
        if (!context_) throw std::runtime_error("llama.cpp could not create the two-sequence context");
        if (!config.draft_model_path.empty()) {
            common_params draft;
            draft.model.path = config.path;
            draft.n_ctx = config.context_tokens;
            draft.n_batch = std::max<std::uint32_t>(config.batch_tokens, 2 * (config.draft_tokens + 1));
            draft.n_ubatch = draft.n_batch;
            draft.n_parallel = 2;
            draft.n_gpu_layers = config.gpu_layers;
            draft.cpuparams.n_threads = config.threads;
            draft.cpuparams_batch.n_threads = config.threads;
            draft.speculative.types = {COMMON_SPECULATIVE_TYPE_DRAFT_DFLASH};
            draft.speculative.draft.mparams.path = config.draft_model_path;
            draft.speculative.draft.n_max = static_cast<std::int32_t>(config.draft_tokens);
            draft.speculative.draft.n_gpu_layers = config.gpu_layers;
            auto init_params = common_base_params_to_speculative(draft);
            speculative_init_ = common_speculative_init_from_params(init_params, model_.get(), context_.get());
            if (!speculative_init_ || !speculative_init_->model() || !speculative_init_->context())
                throw std::runtime_error("llama.cpp could not initialize the two-sequence DFlash model");
            draft.speculative.draft.ctx_tgt = context_.get();
            draft.speculative.draft.ctx_dft = speculative_init_->context();
            speculative_params_ = std::move(draft.speculative);
            speculative_.reset(common_speculative_init(speculative_params_, 2));
            if (!speculative_) throw std::runtime_error("Could not initialize two-sequence DFlash state");
            checkpoint_target_ = common_context_can_seq_rm(context_.get()) == COMMON_CONTEXT_SEQ_RM_TYPE_FULL;
            checkpoint_draft_ = common_context_can_seq_rm(speculative_init_->context()) == COMMON_CONTEXT_SEQ_RM_TYPE_FULL;
        }
        batch_.reset(llama_batch_ext_init(context_.get()));
        if (!batch_) throw std::runtime_error("Could not allocate the shared inference batch");
        initialize_memory_snapshot();
        record_process_memory(MemorySamplePoint::backend_ready);
    }

    std::optional<MemorySnapshot> memory_snapshot() const override {
        std::lock_guard lock(memory_mutex_);
        auto result = memory_snapshot_;
        result.process_samples.clear();
        for (const auto& sample : process_samples_) if (sample) result.process_samples.push_back(*sample);
        return result;
    }

    std::optional<ModelCapabilities> model_capabilities() const override {
        return prompt_formatter_.capabilities;
    }

    std::size_t admit(SequenceId sequence, const GenerationRequest& request) override {
        if (sequence >= 2 || states_.contains(sequence) || request.max_output_tokens == 0 ||
            request.max_output_tokens > config_.max_output_tokens) {
            throw std::invalid_argument("Invalid batch sequence or output bound");
        }
        record_process_memory(MemorySamplePoint::request_start);
        const auto* vocab = llama_model_get_vocab(model_.get());
        auto prepared = prepare_model_prompt(prompt_formatter_, request, vocab);
        const auto& prompt = prepared.prompt;
        const auto needed = llama_tokenize(vocab, prompt.data(), static_cast<int32_t>(prompt.size()),
                                           nullptr, 0, true, true);
        if (needed >= 0 || needed == std::numeric_limits<int32_t>::min()) {
            throw std::runtime_error("Could not determine prompt token count");
        }
        State state;
        state.max_output_tokens = request.max_output_tokens;
        state.tokens.resize(static_cast<std::size_t>(-needed));
        if (llama_tokenize(vocab, prompt.data(), static_cast<int32_t>(prompt.size()), state.tokens.data(),
                           static_cast<int32_t>(state.tokens.size()), true, true) != static_cast<int32_t>(state.tokens.size())) {
            throw std::runtime_error("Prompt tokenization failed");
        }
		state.prepared = std::move(prepared);
		const auto grammar = state.prepared.session && (request.response_format != ResponseFormat::text || request.tool_choice == ToolChoice::required) ? state.prepared.session->grammar() : std::string_view{};
        state.sampler = make_sampler(request, vocab, grammar);
        if (speculative_) {
            common_params_sampling sampling;
            sampling.seed = request.seed.value_or(LLAMA_DEFAULT_SEED);
            sampling.top_k = request.top_k; sampling.top_p = request.top_p; sampling.min_p = 0.0F; sampling.temp = request.temperature;
            if (!grammar.empty()) sampling.grammar = {COMMON_GRAMMAR_TYPE_USER, std::string(grammar)};
            state.speculative_sampler.reset(common_sampler_init(model_.get(), sampling));
            if (!state.speculative_sampler) throw std::runtime_error("Could not allocate speculative sequence sampler");
            state.last = state.tokens.back();
            state.tokens.pop_back();
            state.prompt = state.tokens;
            state.prompt.reserve(config_.context_tokens / 2);
            state.past = static_cast<int>(state.prompt.size());
        }
        state.stop_filter = StopFilter(request.stop);
        if (!state.sampler) throw std::runtime_error("Could not allocate sequence sampler");
        const auto count = state.tokens.size();
        states_.emplace(sequence, std::move(state));
        return count;
    }

    std::vector<BatchStepEvent> step(std::span<const SequenceId> sequences,
                                     std::size_t prefill_chunk_tokens) override {
        if (speculative_) return step_speculative(sequences, prefill_chunk_tokens);
        if (sequences.empty() || sequences.size() > 2 || prefill_chunk_tokens == 0 ||
            prefill_chunk_tokens > config_.batch_tokens) {
            throw std::invalid_argument("Invalid native batch turn");
        }
        llama_batch_ext_clear(batch_.get());
        struct Turn { SequenceId sequence; int32_t logits = -1; bool starts_decode = false; };
        std::vector<Turn> turns;
        turns.reserve(sequences.size());
        std::size_t budget = config_.batch_tokens;
        for (const auto sequence : sequences) {
            auto found = states_.find(sequence);
            if (found == states_.end()) throw std::logic_error("Unknown native sequence");
            turns.push_back({sequence});
            auto& state = found->second;
            if (state.pending_token) {
                const auto position = static_cast<llama_pos>(state.tokens.size() + state.generated - 1);
                turns.back().logits = add_token(sequence, *state.pending_token, position, true);
                state.pending_token.reset();
                --budget;
            }
        }
        std::size_t prompt_count = 0;
        for (const auto sequence : sequences) if (states_.at(sequence).prefill_cursor < states_.at(sequence).tokens.size()) ++prompt_count;
        for (auto& turn : turns) {
            auto& state = states_.at(turn.sequence);
            if (state.prefill_cursor >= state.tokens.size()) continue;
            const auto share = prompt_count ? std::max<std::size_t>(1, budget / prompt_count) : 0;
            const auto amount = std::min({state.tokens.size() - state.prefill_cursor, share,
                                          prefill_chunk_tokens, budget});
            if (amount == 0) throw std::runtime_error("Native batch has no prompt capacity");
            const auto end = state.prefill_cursor + amount;
            for (auto cursor = state.prefill_cursor; cursor < end; ++cursor) {
                const bool final_prompt = cursor + 1 == state.tokens.size();
                const auto index = add_token(turn.sequence, state.tokens[cursor], static_cast<llama_pos>(cursor), final_prompt);
                if (final_prompt) {
                    turn.logits = index;
                    turn.starts_decode = true;
                }
            }
            state.prefill_cursor = end;
            budget -= amount;
            --prompt_count;
        }
        const auto code = llama_process(context_.get(), LLAMA_PROCESS_TYPE_DECODE, batch_.get());
        if (code != 0) throw std::runtime_error("llama.cpp batched evaluation failed with code " + std::to_string(code));
        llama_synchronize(context_.get());

        std::vector<BatchStepEvent> events;
        events.reserve(turns.size());
        const auto* vocab = llama_model_get_vocab(model_.get());
        for (const auto& turn : turns) {
            auto& state = states_.at(turn.sequence);
            BatchStepEvent event{turn.sequence, false, false, {}, false, {}};
            auto append_output = [&](ParsedPiece output) {
                if (output.channel == OutputChannel::tool_call) {
                    for (auto& filtered : state.stop_filter.finish())
                        event.output_pieces.push_back({std::move(filtered.bytes), filtered.channel});
                    event.output_pieces.push_back({std::move(output.bytes), output.channel, output.tool_call_index,
                        std::move(output.tool_call_id), std::move(output.tool_name)});
                } else for (auto& filtered : state.stop_filter.push(std::move(output), state.stopped))
                    event.output_pieces.push_back({std::move(filtered.bytes), filtered.channel});
            };
            if (turn.logits < 0) {
                events.push_back(std::move(event));
                continue;
            }
            if (turn.starts_decode) {
                event.decode_started = true;
                record_process_memory(MemorySamplePoint::prefill_complete);
            }
            const auto token = llama_sampler_sample(state.sampler.get(), context_.get(), turn.logits);
            if (llama_vocab_is_eog(vocab, token)) {
                for (auto& output : finish_parser(state.prepared)) {
                    append_output(std::move(output));
                    if (state.stopped) break;
                }
                if (!state.stopped) for (auto& output : state.stop_filter.finish())
                    event.output_pieces.push_back({std::move(output.bytes), output.channel});
                event.finished = true;
                events.push_back(std::move(event));
                continue;
            }
            event.generated_token = true;
            for (auto& output : feed_parser(state.prepared, token_piece(vocab, token), token)) {
                append_output(std::move(output));
                if (state.stopped) break;
            }
            ++state.generated;
            if (state.stopped) {
                event.finished = true;
            } else if (state.generated >= state.max_output_tokens) {
                for (auto& output : finish_parser(state.prepared)) {
                    append_output(std::move(output));
                    if (state.stopped) break;
                }
                if (!state.stopped) for (auto& output : state.stop_filter.finish())
                    event.output_pieces.push_back({std::move(output.bytes), output.channel});
                event.finished = true;
            }
            else state.pending_token = token;
            events.push_back(std::move(event));
        }
        return events;
    }

    bool remove(SequenceId sequence) noexcept override {
        const auto found = states_.find(sequence);
        if (found == states_.end()) return true;
        record_process_memory(MemorySamplePoint::request_complete_before_cleanup);
        llama_synchronize(context_.get());
        const bool removed = llama_memory_seq_rm(llama_get_memory(context_.get()),
            static_cast<llama_seq_id>(sequence), -1, -1);
        if (speculative_init_) llama_memory_seq_rm(llama_get_memory(speculative_init_->context()),
            static_cast<llama_seq_id>(sequence), -1, -1);
        states_.erase(found);
        record_process_memory(MemorySamplePoint::request_complete_after_cleanup);
        return removed;
    }

private:
    struct State;
    void emit_speculative_token(State& state, BatchStepEvent& event, const llama_vocab* vocab) {
        if (!state.speculative_started) {
            state.speculative_started = true;
            event.decode_started = true;
            record_process_memory(MemorySamplePoint::prefill_complete);
        }
        if (state.accepted.empty()) return;
        const auto token = state.accepted.front(); state.accepted.pop_front();
        auto append_output = [&](ParsedPiece output) {
            if (output.channel == OutputChannel::tool_call) {
                for (auto& filtered : state.stop_filter.finish())
                    event.output_pieces.push_back({std::move(filtered.bytes), filtered.channel});
                event.output_pieces.push_back({std::move(output.bytes), output.channel, output.tool_call_index,
                    std::move(output.tool_call_id), std::move(output.tool_name)});
            } else for (auto& filtered : state.stop_filter.push(std::move(output), state.stopped))
                event.output_pieces.push_back({std::move(filtered.bytes), filtered.channel});
        };
        if (llama_vocab_is_eog(vocab, token)) {
            for (auto& output : finish_parser(state.prepared)) { append_output(std::move(output)); if (state.stopped) break; }
            if (!state.stopped) for (auto& output : state.stop_filter.finish())
                event.output_pieces.push_back({std::move(output.bytes), output.channel});
            state.accepted.clear(); event.finished = true; return;
        }
        event.generated_token = true;
        for (auto& output : feed_parser(state.prepared, token_piece(vocab, token), token)) {
            append_output(std::move(output)); if (state.stopped) break;
        }
        ++state.generated;
        if (state.stopped || state.generated >= state.max_output_tokens) {
            if (!state.stopped) {
                for (auto& output : finish_parser(state.prepared)) { append_output(std::move(output)); if (state.stopped) break; }
                if (!state.stopped) for (auto& output : state.stop_filter.finish())
                    event.output_pieces.push_back({std::move(output.bytes), output.channel});
            }
            state.accepted.clear(); event.finished = true;
        }
    }

    std::vector<BatchStepEvent> step_speculative(std::span<const SequenceId> sequences,
                                                  std::size_t prefill_chunk_tokens) {
        if (sequences.empty() || sequences.size() > 2 || prefill_chunk_tokens == 0 ||
            prefill_chunk_tokens > config_.batch_tokens) throw std::invalid_argument("Invalid speculative batch turn");
        std::vector<BatchStepEvent> events;
        events.reserve(sequences.size());
        for (const auto sequence : sequences) {
            if (!states_.contains(sequence)) throw std::logic_error("Unknown speculative sequence");
            events.push_back({sequence, false, false, {}, false, {}});
        }

        bool has_prefill = false;
        for (const auto sequence : sequences) has_prefill |= states_.at(sequence).prefill_cursor < states_.at(sequence).tokens.size();
        if (has_prefill) {
            common_batch prompt_batch(context_.get());
            std::size_t budget = config_.batch_tokens;
            std::size_t pending = 0;
            for (const auto sequence : sequences) if (states_.at(sequence).prefill_cursor < states_.at(sequence).tokens.size()) ++pending;
            for (const auto sequence : sequences) {
                auto& state = states_.at(sequence);
                if (state.prefill_cursor >= state.tokens.size()) continue;
                const auto share = std::max<std::size_t>(1, budget / pending);
                const auto amount = std::min({state.tokens.size() - state.prefill_cursor, share, prefill_chunk_tokens, budget});
                if (!amount) throw std::runtime_error("Speculative prefill has no batch capacity");
                const auto end = state.prefill_cursor + amount;
                for (auto cursor = state.prefill_cursor; cursor < end; ++cursor)
                    prompt_batch.add(state.tokens[cursor], static_cast<llama_pos>(cursor), static_cast<llama_seq_id>(sequence), false);
                state.prefill_cursor = end; budget -= amount; --pending;
            }
            if (prompt_batch.size() && (llama_process(context_.get(), LLAMA_PROCESS_TYPE_DECODE, prompt_batch.get()) != 0 ||
                !common_speculative_process(speculative_.get(), prompt_batch)))
                throw std::runtime_error("Two-sequence DFlash prompt evaluation failed");
            for (const auto sequence : sequences) {
                auto& state = states_.at(sequence);
                if (state.prefill_cursor == state.tokens.size() && !state.speculative_begun) {
                    common_speculative_begin(speculative_.get(), static_cast<llama_seq_id>(sequence), state.prompt);
                    state.speculative_begun = true;
                }
            }
            return events;
        }

        std::vector<SequenceId> verify;
        for (std::size_t i = 0; i < sequences.size(); ++i) {
            auto& state = states_.at(sequences[i]);
            if (!state.accepted.empty()) emit_speculative_token(state, events[i], llama_model_get_vocab(model_.get()));
            else verify.push_back(sequences[i]);
        }
        if (verify.empty()) return events;

        for (const auto sequence : verify) {
            auto& state = states_.at(sequence);
            if (state.draft.empty()) {
                state.checkpoint.update_pos(state.prompt.size(),
                    llama_memory_seq_pos_min(llama_get_memory(context_.get()), sequence),
                    llama_memory_seq_pos_max(llama_get_memory(context_.get()), sequence));
                if (checkpoint_draft_) state.checkpoint.update_dft(speculative_init_->context(), sequence, LLAMA_STATE_SEQ_FLAGS_PARTIAL_ONLY);
                const auto remaining = state.max_output_tokens - state.generated;
                const auto room = config_.context_tokens / 2 - static_cast<std::uint32_t>(state.past) - 1;
                const auto maximum = std::min({config_.draft_tokens, remaining > 0 ? remaining - 1 : 0U, room});
                common_speculative_get_draft_params(speculative_.get(), sequence) = {
                    .drafting = true, .n_max = static_cast<std::int32_t>(maximum), .pos0 = state.past,
                    .id_last = state.last, .prompt = &state.prompt, .result = &state.draft};
            } else common_speculative_get_draft_params(speculative_.get(), sequence).drafting = false;
        }
        common_speculative_draft(speculative_.get());
        for (const auto sequence : verify) {
            auto& state = states_.at(sequence);
            if (checkpoint_draft_ && !state.checkpoint.load_dft(speculative_init_->context(), sequence, LLAMA_STATE_SEQ_FLAGS_PARTIAL_ONLY))
                throw std::runtime_error("Could not restore two-sequence DFlash checkpoint");
            llama_memory_seq_rm(llama_get_memory(speculative_init_->context()), sequence, state.checkpoint.pos_max + 1, -1);
            if (!state.draft.empty() && checkpoint_target_) state.checkpoint.update_tgt(context_.get(), sequence, LLAMA_STATE_SEQ_FLAGS_PARTIAL_ONLY);
        }

        common_batch target_batch(context_.get());
        std::map<SequenceId, std::vector<int>> indices;
        for (const auto sequence : verify) {
            auto& state = states_.at(sequence);
            indices[sequence].push_back(target_batch.add(state.last, state.past++, sequence, true));
            for (std::size_t i = 0; i < state.draft.size(); ++i)
                indices[sequence].push_back(target_batch.add(state.draft[i], state.past + static_cast<llama_pos>(i), sequence, true));
        }
        if (llama_process(context_.get(), LLAMA_PROCESS_TYPE_DECODE, target_batch.get()) != 0 ||
            !common_speculative_process(speculative_.get(), target_batch))
            throw std::runtime_error("Two-sequence DFlash verification failed");
        const auto* vocab = llama_model_get_vocab(model_.get());
        for (const auto sequence : verify) {
            auto& state = states_.at(sequence);
            auto saved_sampler = checkpoint_target_ ? common_sampler_ptr(common_sampler_clone(state.speculative_sampler.get())) : nullptr;
            const auto draft_count = state.draft.size();
            auto accepted = common_sampler_sample_and_accept_n(state.speculative_sampler.get(), context_.get(), indices[sequence], state.draft);
            const auto found = std::find(sequences.begin(), sequences.end(), sequence);
            auto& event = events[static_cast<std::size_t>(found - sequences.begin())];
            event.speculative_metrics = SpeculativeMetrics{draft_count, accepted.size() - 1};
            if (checkpoint_target_ && accepted.size() - 1 < draft_count) {
                state.draft = std::move(accepted);
                if (!state.checkpoint.load_tgt(context_.get(), sequence, LLAMA_STATE_SEQ_FLAGS_PARTIAL_ONLY) ||
                    !state.checkpoint.load_dft(speculative_init_->context(), sequence, LLAMA_STATE_SEQ_FLAGS_PARTIAL_ONLY))
                    throw std::runtime_error("Could not replay two-sequence DFlash acceptance");
                llama_memory_seq_rm(llama_get_memory(context_.get()), sequence, state.checkpoint.pos_max + 1, -1);
                llama_memory_seq_rm(llama_get_memory(speculative_init_->context()), sequence, state.checkpoint.pos_max + 1, -1);
                state.prompt.resize(state.checkpoint.n_tokens); state.speculative_sampler = std::move(saved_sampler);
                state.past = static_cast<int>(state.prompt.size()); state.replay = true; continue;
            }
            common_speculative_accept(speculative_.get(), sequence, static_cast<std::uint16_t>(accepted.size() - 1));
            state.past += static_cast<int>(accepted.size() - 1);
            for (const auto token : accepted) { state.prompt.push_back(state.last); state.last = token; state.accepted.push_back(token); }
            state.draft.clear(); state.replay = false;
            llama_memory_seq_rm(llama_get_memory(context_.get()), sequence, state.past, -1);
            llama_memory_seq_rm(llama_get_memory(speculative_init_->context()), sequence, state.past, -1);
            emit_speculative_token(state, event, vocab);
        }
        return events;
    }

    struct State {
        std::vector<llama_token> tokens;
        std::size_t prefill_cursor = 0;
        std::uint32_t generated = 0;
        std::uint32_t max_output_tokens = 0;
        std::optional<llama_token> pending_token;
        Owned<llama_sampler, llama_sampler_free> sampler{nullptr, llama_sampler_free};
        common_sampler_ptr speculative_sampler;
        PreparedPrompt prepared;
        StopFilter stop_filter;
        bool stopped = false;
        llama_tokens prompt;
        llama_tokens draft;
        std::deque<llama_token> accepted;
        common_prompt_checkpoint checkpoint;
        llama_token last = LLAMA_TOKEN_NULL;
        int past = 0;
        bool speculative_started = false;
        bool speculative_begun = false;
        bool replay = false;
    };

    int32_t add_token(SequenceId sequence, llama_token token, llama_pos position, bool logits) {
        const auto index = llama_batch_ext_add_token(batch_.get(), static_cast<llama_seq_id>(sequence), token);
        if (index < 0 || !llama_batch_ext_set_pos(batch_.get(), index, &position) ||
            !llama_batch_ext_set_output_logits(batch_.get(), index, logits)) {
            throw std::runtime_error("Could not construct multi-sequence inference batch");
        }
        return index;
    }

    static std::string token_piece(const llama_vocab* vocab, llama_token token) {
        std::array<char, 256> buffer{};
        const auto bytes = llama_token_to_piece(vocab, token, buffer.data(), buffer.size(), 0, false);
        if (bytes == std::numeric_limits<int32_t>::min()) throw std::runtime_error("Token piece length overflow");
        if (bytes < 0) {
            std::string piece(static_cast<std::size_t>(-bytes), '\0');
            if (llama_token_to_piece(vocab, token, piece.data(), -bytes, 0, false) != -bytes) {
                throw std::runtime_error("Token conversion failed");
            }
            return piece;
        }
        return std::string(buffer.data(), static_cast<std::size_t>(bytes));
    }

    static std::size_t sample_index(MemorySamplePoint point) { return static_cast<std::size_t>(point) - 1; }
    void record_process_memory(MemorySamplePoint point) noexcept {
        const auto sample = capture_process_memory(point);
        std::lock_guard lock(memory_mutex_);
        process_samples_[sample_index(point)] = sample;
    }
    void initialize_memory_snapshot() {
        memory_snapshot_.configured_context_tokens = config_.context_tokens;
        memory_snapshot_.configured_batch_tokens = config_.batch_tokens;
        memory_snapshot_.configured_sequences = 2;
        memory_snapshot_.configured_gpu_layers = config_.gpu_layers;
        memory_snapshot_.model_tensor_bytes = llama_model_size(model_.get());
        const auto breakdown = llama_get_memory_breakdown(context_.get());
        if (breakdown.size() > 32) return;
        std::uint64_t model_total = 0, context_total = 0, compute_total = 0;
        auto add = [](std::uint64_t& total, std::size_t value) {
            if (value > std::numeric_limits<std::uint64_t>::max() - total) return false;
            total += static_cast<std::uint64_t>(value); return true;
        };
        for (const auto& [type, bytes] : breakdown) {
            const char* raw_name = ggml_backend_buft_name(type);
            std::string name = raw_name ? raw_name : "unknown";
            if (name.size() > 128) name.resize(128);
            if (!add(model_total, bytes.model) || !add(context_total, bytes.context) || !add(compute_total, bytes.compute)) {
                memory_snapshot_.llama_buffers.clear(); return;
            }
            memory_snapshot_.llama_buffers.push_back({std::move(name), bytes.model, bytes.context, bytes.compute});
        }
        memory_snapshot_.llama_model_allocated_bytes = model_total;
        memory_snapshot_.llama_context_allocated_bytes = context_total;
        memory_snapshot_.llama_compute_allocated_bytes = compute_total;
    }

    LlamaModelConfig config_;
    BackendSession session_;
    Owned<llama_model, llama_model_free> model_{nullptr, llama_model_free};
    Owned<llama_context, llama_free> context_{nullptr, llama_free};
    Owned<llama_batch_ext, llama_batch_ext_free> batch_{nullptr, llama_batch_ext_free};
    common_speculative_init_result_ptr speculative_init_;
    common_params_speculative speculative_params_;
    common_speculative_ptr speculative_;
    bool checkpoint_target_ = false;
    bool checkpoint_draft_ = false;
    PromptFormatter prompt_formatter_;
    std::map<SequenceId, State> states_;
    mutable std::mutex memory_mutex_;
    MemorySnapshot memory_snapshot_;
    std::array<std::optional<ProcessMemorySample>, 5> process_samples_;
};
} // namespace

std::unique_ptr<InferenceBackend> make_llama_backend(const LlamaModelConfig& config) {
    if (!std::filesystem::is_regular_file(config.path) || config.threads < 1 ||
        config.context_tokens < 32 || config.context_tokens > 1048576 ||
        config.max_output_tokens == 0 || config.max_output_tokens > config.context_tokens ||
        config.batch_tokens == 0 || config.batch_tokens > config.context_tokens || config.gpu_layers < 0 ||
        (!config.draft_model_path.empty() &&
         (!std::filesystem::is_regular_file(config.draft_model_path) || config.draft_tokens == 0 ||
          config.draft_tokens > 32))) {
        throw std::invalid_argument("Invalid local model path or execution configuration");
    }
    return std::make_unique<LlamaBackend>(config);
}

std::unique_ptr<BatchExecutor> make_llama_batch_executor(const LlamaModelConfig& config) {
    if (!std::filesystem::is_regular_file(config.path) || config.threads < 1 ||
        config.context_tokens < 64 || config.context_tokens > 1048576 ||
        config.max_output_tokens == 0 || config.max_output_tokens > config.context_tokens / 2 ||
        config.batch_tokens != 128 || config.gpu_layers < 0 ||
        (!config.draft_model_path.empty() &&
         (!std::filesystem::is_regular_file(config.draft_model_path) || config.draft_tokens == 0 || config.draft_tokens > 32))) {
        throw std::invalid_argument("Invalid conservative two-sequence model configuration");
    }
    return std::make_unique<LlamaBatchExecutor>(config);
}
} // namespace inferno

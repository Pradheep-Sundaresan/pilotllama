#include "inferno/llama_backend.hpp"
#include "inferno/model_prompt.hpp"
#include "inferno/nemotron_prompt.hpp"
#include "chat.h"
#include "common.h"
#include "llama-ext.h"
#include "llama.h"
#include "ggml-backend.h"

#include <algorithm>
#include <array>
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
};

std::string render_embedded_template(const common_chat_templates* templates, std::string_view user_prompt) {
    common_chat_templates_inputs inputs;
    inputs.enable_thinking = false;
    inputs.messages.emplace_back();
    inputs.messages.back().role = "user";
    inputs.messages.back().content = user_prompt;
    const auto rendered = common_chat_templates_apply(templates, inputs).prompt.str();
    if (rendered.empty()) {
        throw std::invalid_argument("GGUF chat template rendered an empty prompt");
    }
    if (rendered.size() > max_formatted_prompt_bytes) {
        throw std::invalid_argument("Formatted prompt exceeds the 1 MiB safety limit");
    }
    return rendered;
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
        raw_template != nullptr && raw_template[0] != '\0'
    };
    PromptFormatter result;
    result.mode = select_model_prompt_mode(metadata);
    result.continuous_batching_verified = supports_verified_continuous_batching(metadata);
    if (result.mode == ModelPromptMode::embedded_template) {
        if (std::strlen(raw_template) > max_formatted_prompt_bytes) {
            throw std::invalid_argument("GGUF chat template exceeds the 1 MiB safety limit");
        }
        result.embedded_templates = common_chat_templates_init(model, "");
        if (!result.embedded_templates) throw std::invalid_argument("Could not initialize the GGUF chat template");
        (void) render_embedded_template(result.embedded_templates.get(), "template compatibility check");
    }
    return result;
}

std::string format_model_prompt(const PromptFormatter& formatter, std::string_view user_prompt) {
    validate_user_prompt(user_prompt);
    if (formatter.mode == ModelPromptMode::verified_nemotron_fallback) {
        return format_nemotron_prompt(user_prompt);
    }
    return render_embedded_template(formatter.embedded_templates.get(), user_prompt);
}

struct BackendSession {
    BackendSession() { llama_backend_init(); }
    ~BackendSession() { llama_backend_free(); }
};

template <typename T, void (*Free)(T*)>
using Owned = std::unique_ptr<T, decltype(Free)>;

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
        auto params = llama_context_default_params();
        params.n_ctx = config.context_tokens;
        params.n_batch = config.batch_tokens;
        params.n_ubatch = config.batch_tokens;
        params.n_threads = config.threads;
        params.n_threads_batch = config.threads;
        params.n_seq_max = 1;
        context_.reset(llama_init_from_model(model_.get(), params));
        if (!context_) {
            throw std::runtime_error("llama.cpp could not create the execution context");
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

    void generate(const GenerationRequest& request, const DecodeStarted& decode_started,
                  const TokenCallback& emit_token, const Checkpoint& checkpoint) override {
        record_process_memory(MemorySamplePoint::request_start);
        checkpoint();
        if (request.max_output_tokens == 0 || request.max_output_tokens > 512) {
            throw std::invalid_argument("Use 1-512 output tokens");
        }
        const auto prompt = format_model_prompt(prompt_formatter_, request.prompt);
        const auto* vocab = llama_model_get_vocab(model_.get());
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
        ClearState reset{context_.get(), [this](MemorySamplePoint point) { record_process_memory(point); }};
        Owned<llama_sampler, llama_sampler_free> sampler(llama_sampler_init_greedy(), llama_sampler_free);
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
        for (std::uint32_t generated = 0; generated < request.max_output_tokens; ++generated) {
            checkpoint();
            const auto token = llama_sampler_sample(sampler.get(), context_.get(), -1);
            if (llama_vocab_is_eog(vocab, token)) {
                break;
            }
            std::array<char, 256> buffer{};
            const auto bytes = llama_token_to_piece(vocab, token, buffer.data(), buffer.size(), 0, false);
            if (bytes < 0) {
                if (bytes == std::numeric_limits<int32_t>::min()) {
                    throw std::runtime_error("Token piece length overflow");
                }
                std::string piece(static_cast<std::size_t>(-bytes), '\0');
                if (llama_token_to_piece(vocab, token, piece.data(), -bytes, 0, false) != -bytes) {
                    throw std::runtime_error("Token conversion failed");
                }
                emit_token(piece);
            } else {
                emit_token(std::string_view(buffer.data(), static_cast<std::size_t>(bytes)));
            }
            if (generated + 1 < request.max_output_tokens) {
                llama_batch_ext_clear(batch.get());
                add(token, static_cast<llama_pos>(count + generated), true);
                evaluate();
            }
        }
    }

private:
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
        memory_snapshot_.model_tensor_bytes = llama_model_size(model_.get());
        const auto breakdown = llama_get_memory_breakdown(context_.get());
        constexpr std::size_t row_limit = 32;
        if (breakdown.size() > row_limit) return;
        std::uint64_t model_total = 0, context_total = 0, compute_total = 0;
        auto add = [](std::uint64_t& total, std::size_t value) {
            if (value > std::numeric_limits<std::uint64_t>::max() - total) return false;
            total += static_cast<std::uint64_t>(value);
            return true;
        };
        for (const auto& [type, bytes] : breakdown) {
            const char* raw_name = ggml_backend_buft_name(type);
            std::string name = raw_name ? raw_name : "unknown";
            if (name.size() > 128) name.resize(128);
            if (!add(model_total, bytes.model) || !add(context_total, bytes.context) ||
                !add(compute_total, bytes.compute)) {
                memory_snapshot_.llama_buffers.clear();
                return;
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
    PromptFormatter prompt_formatter_;
    mutable std::mutex memory_mutex_;
    MemorySnapshot memory_snapshot_;
    std::array<std::optional<ProcessMemorySample>, 5> process_samples_;
};

class LlamaBatchExecutor final : public BatchExecutor {
public:
    explicit LlamaBatchExecutor(const LlamaModelConfig& config) : config_(config) {
        auto model_params = llama_model_default_params();
        model_params.n_gpu_layers = config.gpu_layers;
        model_.reset(llama_model_load_from_file(config.path.c_str(), model_params));
        if (!model_) throw std::runtime_error("llama.cpp could not load the local GGUF");
        prompt_formatter_ = inspect_prompt_formatter(model_.get());
        if (!prompt_formatter_.continuous_batching_verified) {
            throw std::invalid_argument("Continuous batching is not verified for this model");
        }
        auto params = llama_context_default_params();
        params.n_ctx = config.context_tokens;
        params.n_batch = config.batch_tokens;
        params.n_ubatch = config.batch_tokens;
        params.n_threads = config.threads;
        params.n_threads_batch = config.threads;
        params.n_seq_max = 2;
        context_.reset(llama_init_from_model(model_.get(), params));
        if (!context_) throw std::runtime_error("llama.cpp could not create the two-sequence context");
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

    std::size_t admit(SequenceId sequence, const GenerationRequest& request) override {
        if (sequence >= 2 || states_.contains(sequence) || request.max_output_tokens == 0 ||
            request.max_output_tokens > 512) {
            throw std::invalid_argument("Invalid batch sequence or output bound");
        }
        record_process_memory(MemorySamplePoint::request_start);
        const auto prompt = format_model_prompt(prompt_formatter_, request.prompt);
        const auto* vocab = llama_model_get_vocab(model_.get());
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
        state.sampler.reset(llama_sampler_init_greedy());
        if (!state.sampler) throw std::runtime_error("Could not allocate sequence sampler");
        const auto count = state.tokens.size();
        states_.emplace(sequence, std::move(state));
        return count;
    }

    std::vector<BatchStepEvent> step(std::span<const SequenceId> sequences,
                                     std::size_t prefill_chunk_tokens) override {
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
            BatchStepEvent event{turn.sequence, false, std::nullopt, false, {}};
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
                event.finished = true;
                events.push_back(std::move(event));
                continue;
            }
            event.token_piece = token_piece(vocab, token);
            ++state.generated;
            if (state.generated >= state.max_output_tokens) event.finished = true;
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
        states_.erase(found);
        record_process_memory(MemorySamplePoint::request_complete_after_cleanup);
        return removed;
    }

private:
    struct State {
        std::vector<llama_token> tokens;
        std::size_t prefill_cursor = 0;
        std::uint32_t generated = 0;
        std::uint32_t max_output_tokens = 0;
        std::optional<llama_token> pending_token;
        Owned<llama_sampler, llama_sampler_free> sampler{nullptr, llama_sampler_free};
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
    PromptFormatter prompt_formatter_;
    std::map<SequenceId, State> states_;
    mutable std::mutex memory_mutex_;
    MemorySnapshot memory_snapshot_;
    std::array<std::optional<ProcessMemorySample>, 5> process_samples_;
};
} // namespace

std::unique_ptr<InferenceBackend> make_llama_backend(const LlamaModelConfig& config) {
    if (!std::filesystem::is_regular_file(config.path) || config.threads < 1 ||
        config.context_tokens < 32 || config.context_tokens > 4096 ||
        config.batch_tokens == 0 || config.batch_tokens > config.context_tokens || config.gpu_layers < 0) {
        throw std::invalid_argument("Invalid local model path or execution configuration");
    }
    return std::make_unique<LlamaBackend>(config);
}

std::unique_ptr<BatchExecutor> make_llama_batch_executor(const LlamaModelConfig& config) {
    if (!std::filesystem::is_regular_file(config.path) || config.threads < 1 ||
        config.context_tokens != 4096 || config.batch_tokens != 128 || config.gpu_layers < 0) {
        throw std::invalid_argument("Invalid conservative two-sequence model configuration");
    }
    return std::make_unique<LlamaBatchExecutor>(config);
}
} // namespace inferno

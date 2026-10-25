#include "inferno/model_prompt.hpp"
#include "inferno/runtime.hpp"
#ifdef INFERNO_WITH_LLAMA
#include "inferno/llama_backend.hpp"
#endif

#include <charconv>
#include <iostream>
#include <stdexcept>
#include <string>
#include <string_view>
#include <utility>

namespace {
void print_help() {
    std::cout << "INFERNO — Local AI Inference Platform\n"
                 "Usage: inferno [--help | --version]\n"
                 "       inferno generate --model PATH --prompt TEXT [--max-tokens N]\n"
                 "                        [--context-tokens 32..1048576]\n"
                 "                        [--stream] [--metrics] [--timeout-ms 1..3600000]\n\n"
                 "Generation requires the native build and a supported local GGUF.\n";
}
} // namespace

int main(int argc, char* argv[]) {
    if (argc == 1 || (argc == 2 && std::string_view{argv[1]} == "--help")) {
        print_help();
        return 0;
    }
    if (argc == 2 && std::string_view{argv[1]} == "--version") {
        std::cout << "inferno " << INFERNO_VERSION << '\n';
        return 0;
    }
    if (std::string_view{argv[1]} == "generate") {
        std::string model;
        std::string prompt;
        std::uint32_t max_tokens = 64;
        std::uint32_t context_tokens = 4096;
        std::optional<std::uint32_t> timeout_ms;
        bool have_model = false, have_prompt = false, have_limit = false, have_context = false;
        bool stream = false, metrics = false;
        for (int i = 2; i < argc;) {
            const std::string_view key{argv[i]};
            if ((key == "--stream" && !stream) || (key == "--metrics" && !metrics)) {
                if (key == "--stream") stream = true;
                else metrics = true;
                ++i;
                continue;
            }
            if (i + 1 >= argc) {
                std::cerr << "Missing option value. Use inferno --help.\n";
                return 2;
            }
            const std::string_view value{argv[i + 1]};
            if (key == "--model" && !have_model) {
                model = value;
                have_model = true;
            } else if (key == "--prompt" && !have_prompt) {
                prompt = value;
                have_prompt = true;
            } else if (key == "--max-tokens" && !have_limit) {
                const auto parsed = std::from_chars(value.data(), value.data() + value.size(), max_tokens);
                if (parsed.ec != std::errc{} || parsed.ptr != value.data() + value.size() ||
                    max_tokens == 0 || max_tokens > 1048576) {
                    std::cerr << "--max-tokens must be an integer from 1 to 1048576.\n";
                    return 2;
                }
                have_limit = true;
            } else if (key == "--context-tokens" && !have_context) {
                const auto parsed = std::from_chars(value.data(), value.data() + value.size(), context_tokens);
                if (parsed.ec != std::errc{} || parsed.ptr != value.data() + value.size() ||
                    context_tokens < 32 || context_tokens > 1048576) {
                    std::cerr << "--context-tokens must be an integer from 32 to 1048576.\n";
                    return 2;
                }
                have_context = true;
            } else if (key == "--timeout-ms" && !timeout_ms) {
                std::uint32_t value_ms = 0;
                const auto parsed = std::from_chars(value.data(), value.data() + value.size(), value_ms);
                if (parsed.ec != std::errc{} || parsed.ptr != value.data() + value.size() ||
                    value_ms == 0 || value_ms > 3600000) {
                    std::cerr << "--timeout-ms must be an integer from 1 to 3600000.\n";
                    return 2;
                }
                timeout_ms = value_ms;
            } else {
                std::cerr << "Unknown or duplicate option. Use inferno --help.\n";
                return 2;
            }
            i += 2;
        }
        if (!have_model || model.empty() || !have_prompt) {
            std::cerr << "generate requires --model and --prompt.\n";
            return 2;
        }
        if (max_tokens > context_tokens) {
            std::cerr << "--max-tokens must fit inside --context-tokens.\n";
            return 2;
        }
        try {
            inferno::validate_user_prompt(prompt);
#ifdef INFERNO_WITH_LLAMA
            inferno::Runtime runtime(inferno::make_llama_backend({.path = model,
                .context_tokens = context_tokens, .max_output_tokens = max_tokens}), 2);
            inferno::InferenceBackend::TokenCallback token;
            if (stream) {
                token = [](inferno::GeneratedPiece piece) {
                    std::cout.write(piece.bytes.data(), static_cast<std::streamsize>(piece.bytes.size()));
                    std::cout.flush();
                    if (!std::cout) throw std::runtime_error("Output stream write failed");
                };
            }
            std::optional<inferno::RequestClock::time_point> deadline;
            if (timeout_ms) deadline = inferno::RequestClock::now() + std::chrono::milliseconds(*timeout_ms);
            auto submission = runtime.submit({1, prompt, max_tokens, deadline, {}}, std::move(token));
            const auto result = submission.completion.get();
            if (metrics) std::cerr << inferno::metrics_json(result.id, result.state, result.metrics) << '\n';
            if (result.state != inferno::RequestState::completed) {
                throw std::runtime_error(result.error);
            }
            if (!stream) std::cout << result.text;
            std::cout << '\n';
            std::cerr << "request " << result.id << " " << inferno::to_string(result.state) << '\n';
            return 0;
#else
            std::cerr << "Configure and build the native preset to enable generation.\n";
            return 1;
#endif
        } catch (const std::exception& error) {
            std::cerr << "Generation failed: " << error.what() << '\n';
            return 1;
        }
    }
    std::cerr << "Unsupported arguments. Use inferno --help.\n";
    return 2;
}

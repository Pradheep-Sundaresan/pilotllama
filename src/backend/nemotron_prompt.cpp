#include "inferno/nemotron_prompt.hpp"
#include "inferno/model_prompt.hpp"
#include <stdexcept>

namespace inferno {
std::string format_nemotron_prompt(std::string_view user_prompt) {
    validate_user_prompt(user_prompt);
    if (user_prompt.find("<|im_start|>") != std::string_view::npos ||
        user_prompt.find("<|im_end|>") != std::string_view::npos) {
        throw std::invalid_argument("User prompt must not contain chat control delimiters");
    }
    return "<|im_start|>system\n<|im_end|>\n<|im_start|>user\n" +
           std::string(user_prompt) + "<|im_end|>\n<|im_start|>assistant\n<think></think>";
}
} // namespace inferno

#pragma once
#include <string>
#include <string_view>

namespace inferno {
// Fixed single-turn, no-tools, thinking-disabled format for the documented 4B model.
[[nodiscard]] std::string format_nemotron_prompt(std::string_view user_prompt);
} // namespace inferno

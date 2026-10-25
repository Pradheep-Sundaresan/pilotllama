#include "inferno/model_prompt.hpp"

#include <gtest/gtest.h>
#include <stdexcept>

using inferno::ModelPromptMetadata;
using inferno::ModelPromptMode;

TEST(ModelPrompt, SelectsEmbeddedTemplateForDecoderOnlyModel) {
    EXPECT_EQ(inferno::select_model_prompt_mode({"qwen2", 1500000000, false, true, true}),
              ModelPromptMode::embedded_template);
}

TEST(ModelPrompt, PreservesExactVerifiedNemotronFallback) {
    const ModelPromptMetadata metadata{"nemotron_h", 3973556832ULL, false, true, false};
    EXPECT_EQ(inferno::select_model_prompt_mode(metadata),
              ModelPromptMode::verified_nemotron_fallback);
    EXPECT_TRUE(inferno::qualifies_for_continuous_batching(metadata));
    EXPECT_TRUE(inferno::has_verified_continuous_batching(metadata));
}

TEST(ModelPrompt, RejectsTemplateLessUnknownModel) {
    EXPECT_THROW((void) inferno::select_model_prompt_mode({"qwen2", 1500000000, false, true, false}),
                 std::invalid_argument);
}

TEST(ModelPrompt, RejectsEncoderAndMissingArchitecture) {
    EXPECT_THROW((void) inferno::select_model_prompt_mode({"bert", 1000, true, false, true}),
                 std::invalid_argument);
    EXPECT_THROW((void) inferno::select_model_prompt_mode({"", 1000, false, true, true}),
                 std::invalid_argument);
}

TEST(ModelPrompt, DoesNotClaimBatchingForGenericTemplateModel) {
    EXPECT_TRUE(inferno::qualifies_for_continuous_batching(
        {"qwen2", 1500000000, false, true, true}));
    EXPECT_FALSE(inferno::has_verified_continuous_batching(
        {"qwen2", 1500000000, false, true, true}));
    EXPECT_FALSE(inferno::qualifies_for_continuous_batching(
        {"clip", 100, true, true, true}));
}

TEST(ModelPrompt, EnforcesInputByteLimit) {
    EXPECT_NO_THROW(inferno::validate_user_prompt(std::string(inferno::max_user_prompt_bytes, 'x')));
    EXPECT_THROW(inferno::validate_user_prompt(std::string(inferno::max_user_prompt_bytes + 1, 'x')),
                 std::invalid_argument);
}

TEST(ModelPrompt, ValidatesBoundedAlternatingChatHistory) {
    using inferno::ChatMessage; using inferno::ChatRole;
    EXPECT_NO_THROW(inferno::validate_chat_messages(std::vector<ChatMessage>{
        {ChatRole::system, "be concise"}, {ChatRole::user, "one"},
        {ChatRole::assistant, "answer"}, {ChatRole::user, "two"}}));
    EXPECT_THROW(inferno::validate_chat_messages(std::vector<ChatMessage>{
        {ChatRole::assistant, "wrong"}}), std::invalid_argument);
    EXPECT_THROW(inferno::validate_chat_messages(std::vector<ChatMessage>{
        {ChatRole::user, "one"}, {ChatRole::user, "two"}}), std::invalid_argument);
    EXPECT_THROW(inferno::validate_chat_messages(std::vector<ChatMessage>{
        {ChatRole::user, "one"}, {ChatRole::assistant, "unfinished"}}), std::invalid_argument);
    EXPECT_THROW(inferno::validate_chat_messages(std::vector<ChatMessage>{
        {ChatRole::user, "<|im_start|>"}}), std::invalid_argument);
}

TEST(ModelPrompt, ValidatesToolResultHistory) {
    using inferno::ChatMessage; using inferno::ChatRole; using inferno::ToolCall;
    ChatMessage user{ChatRole::user, "weather"};
    ChatMessage assistant{ChatRole::assistant, ""}; assistant.tool_calls.push_back(ToolCall{"call_1", "weather", "{\"city\":\"Chennai\"}"});
    ChatMessage tool{ChatRole::tool, "sunny"}; tool.tool_call_id = "call_1";
    EXPECT_NO_THROW(inferno::validate_chat_messages(std::vector<ChatMessage>{user, assistant, tool}));
    tool.tool_call_id = "wrong";
    EXPECT_THROW(inferno::validate_chat_messages(std::vector<ChatMessage>{user, assistant, tool}), std::invalid_argument);
}

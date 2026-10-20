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
    EXPECT_TRUE(inferno::supports_verified_continuous_batching(metadata));
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
    EXPECT_FALSE(inferno::supports_verified_continuous_batching(
        {"qwen2", 1500000000, false, true, true}));
}

TEST(ModelPrompt, EnforcesInputByteLimit) {
    EXPECT_NO_THROW(inferno::validate_user_prompt(std::string(inferno::max_user_prompt_bytes, 'x')));
    EXPECT_THROW(inferno::validate_user_prompt(std::string(inferno::max_user_prompt_bytes + 1, 'x')),
                 std::invalid_argument);
}

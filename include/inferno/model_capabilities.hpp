#pragma once

#include <cstdint>
#include <string>

namespace inferno {

enum class CapabilitySupport { unavailable, detected, verified, unqualified };
enum class CompatibilityLevel { compatible, verified };
enum class PromptTemplateSource { embedded, verified_fallback };

// Load-time facts and bounded template probes. "Detected" means the pinned
// llama.cpp renderer accepted the feature shape; it is not a generation claim.
struct ModelCapabilities {
    std::string architecture;
    std::uint64_t parameter_count = 0;
    std::uint32_t trained_context_tokens = 0;
    std::uint32_t configured_context_tokens = 0;
    std::uint32_t configured_max_output_tokens = 0;
    bool has_encoder = false;
    bool has_decoder = false;
    bool has_embedded_chat_template = false;
    CompatibilityLevel compatibility = CompatibilityLevel::compatible;
    PromptTemplateSource template_source = PromptTemplateSource::embedded;
    CapabilitySupport queue_mode = CapabilitySupport::detected;
    CapabilitySupport continuous_mode = CapabilitySupport::unqualified;
    CapabilitySupport system_messages = CapabilitySupport::unqualified;
    CapabilitySupport multi_turn = CapabilitySupport::unqualified;
    CapabilitySupport thinking = CapabilitySupport::unqualified;
    CapabilitySupport tools = CapabilitySupport::unqualified;
};

} // namespace inferno

#pragma once

#include "inferno/request.hpp"
#include "inferno/memory.hpp"
#include "inferno/model_capabilities.hpp"

#include <functional>
#include <optional>
#include <string>
#include <string_view>

namespace inferno {

// Called synchronously by one runtime worker. Notify decoding once, just before
// decode begins; omit it for zero-output completion. Never retain the callback or
// request references, or invoke shutdown/destruction of the calling runtime.
class InferenceBackend {
public:
    using DecodeStarted = std::function<void()>;
    // Exactly one synchronous event per generated non-EOG token, even for empty
    // byte pieces. The view expires on return; callback exceptions abort this request.
    using TokenCallback = std::function<void(GeneratedPiece)>;
    using Checkpoint = std::function<void()>;
    virtual ~InferenceBackend() = default;
    // Implementations must return a bounded copy safe to read during generation.
    virtual std::optional<MemorySnapshot> memory_snapshot() const { return std::nullopt; }
    virtual std::optional<ModelCapabilities> model_capabilities() const { return std::nullopt; }
    virtual void generate(const GenerationRequest& request, const DecodeStarted& decode_started,
                          const TokenCallback& token, const Checkpoint& checkpoint) = 0;
};

} // namespace inferno

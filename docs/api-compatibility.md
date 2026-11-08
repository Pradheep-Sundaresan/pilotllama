# API compatibility

PilotLlama is OpenAI-style, not a drop-in implementation of every OpenAI API. This
matrix describes the current public surface so integrations can fail explicitly.

| Surface | Status | Notes |
| --- | --- | --- |
| Bearer authentication | Supported | One local service token; no users/roles yet |
| `GET /v1/models` | Supported subset | One live READY alias; `created` is null |
| `POST /v1/chat/completions` JSON | Supported subset | One choice only |
| Chat Completions SSE | Supported subset | Role/content/final chunks and `[DONE]` |
| Multi-turn text messages | Supported | Template- and capability-gated |
| Sampling | Supported subset | temperature, top-p, top-k, seed, stop |
| Reasoning | PilotLlama extension | Opt-in `thinking`; separate `reasoning_content` |
| Function tools | Supported subset | Typed deltas/results; PilotLlama does not execute tools |
| Structured output | Supported subset | `json_object` and bounded strict JSON schema |
| Standard stream `usage` | Unsupported | Prompt tokens are not measured; no totals are fabricated |
| Measured stream telemetry | PilotLlama extension | `pilotllama.include_metrics` emits `pilotllama.metrics` |
| `n > 1` | Unsupported | Exactly one choice |
| Embeddings | Unsupported | No embeddings endpoint |
| Responses/Assistants APIs | Unsupported | Chat Completions only |
| Images, audio, multimodal input | Unsupported | Text-only runtime boundary |
| Remote/public serving | Unsupported | Explicit IPv4 loopback only |

Unknown request fields and unsupported values are rejected. Applications should
depend only on the documented contract, not on accidental similarity to another API.

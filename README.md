# PilotLlama

Local LLM inference infrastructure for Apple Silicon.

PilotLlama serves a user-supplied GGUF through an OpenAI-style streaming API. Go
owns the control plane; C++20 owns bounded scheduling and llama.cpp/Metal execution;
React exposes the system in a local observability Studio.

![PilotLlama Studio Console showing two active local generations, scheduler slots, measured decode rates, and memory](docs/assets/pilotllama-studio-console.png)

![PilotLlama system architecture](docs/assets/pilotllama-architecture.svg)

## Why PilotLlama?

Ollama makes it easy to download and run a local model. PilotLlama helps you run a
small, visible local AI service for an application.

- Your app gets a local OpenAI-style API with JSON and live streaming output.
- You can see whether requests are executing or waiting, rather than guessing why a
  response feels slow.
- You can inspect first-token time, generation speed, queue wait, memory, and backend
  health for each real request.
- You can drain requests, safely stop/start the managed backend, and test queue versus
  continuous scheduling from one local control surface.

In plain English: Ollama is excellent for running a model. PilotLlama is for running
and understanding a local AI service. It has a deliberately narrower model boundary;
Ollama remains broader for model discovery, downloads, and general compatibility.

The comparison below describes product surfaces, not a universal performance ranking.
`llama-server` itself supports parallel decoding, continuous batching, reasoning,
tools, schemas and metrics; PilotLlama builds a deliberately bounded, inspected
service around pinned llama.cpp rather than claiming those primitives as inventions.

| Dimension | `llama.cpp` / `llama-server` | Ollama | PilotLlama |
| :--- | :--- | :--- | :--- |
| **Primary philosophy** | Portable inference engine plus configurable server | Easy model acquisition and local serving | Visible, predictable local AI service for applications |
| **Architecture** | C/C++ library, CLI and optional HTTP server | Go daemon with managed native runners | Go control plane ↔ localhost gRPC ↔ owned C++20 data plane |
| **Scheduling and concurrency** | Configurable parallel slots and continuous batching | Daemon-managed parallelism and queueing | Explicit bounded queue or qualified two-sequence continuous mode with two waiting slots |
| **Concurrent TTFT evidence** | Not compared by this project | Not compared by this project | Controlled two-client run reduced median TTFT 77% versus PilotLlama queue mode; not a universal or P95 claim |
| **Memory policy** | Operator-selected context, batch and cache flags | Configurable context and parallelism | Startup/request budget enforcement, measured allocations and labelled unified-memory presets |
| **Process lifecycle** | Deployment-owned | Daemon-managed runners | Go remains available while its supervised native child is stopped/restarted; readiness blocks traffic |
| **Client disconnects** | Endpoint-dependent; not evaluated here | Not evaluated here | Verified cooperative cancellation and native sequence cleanup |
| **Reasoning models** | Supports parsed `reasoning_content` for supported templates | Supports thinking-capable models | Capability-gated, separately streamed `reasoning_content`; never inferred by searching raw tags |
| **Tools and schemas** | Function calling and grammar-constrained JSON | Tool calling and structured outputs | Bounded definitions, typed indexed OpenAI deltas, tool-result history and JSON-schema grammar validation |
| **Model compatibility** | Broad GGUF/backend surface with operator configuration | Broad curated library and model workflow | Decoder-only GGUFs must load through the pinned runtime; bounded Driving Test records exact-artifact evidence |
| **Observability** | Logs plus optional server metrics/slot endpoints | Operational model/runtime status | Studio: per-request TTFT, queue wait, decode rate, lifecycle, scheduler and separated memory accounting |

See the current [`llama-server` feature surface](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md)
and [Ollama documentation](https://docs.ollama.com/) for their respective capabilities.

## Capabilities

- `POST /v1/chat/completions` with JSON or SSE streaming
- bearer auth, token-bucket rate limiting, drain/resume, readiness and managed restart
- localhost gRPC server-streaming between Go and C++
- bounded queue mode or opt-in two-sequence continuous batching
- optional qualified DFlash speculative decoding in queue or two-sequence continuous mode
- per-request DFlash drafted/accepted-token and acceptance-rate telemetry
- cooperative deadlines, disconnect cancellation and per-sequence cleanup
- structured 1–64-message chat with optional system message and alternating
  user/assistant history, rendered by supported embedded templates
- capability-gated reasoning separated from final content in `reasoning_content`
- bounded OpenAI function definitions, typed tool-call deltas and tool-result history
- `json_object` and bounded `json_schema` constrained final output
- temperature, top-p, top-k, uint32 seed and up to four semantic stop sequences
- configurable context/output budgets with 4K, 8K, 32K and custom memory presets
- exact-artifact qualification for generic two-sequence continuous batching
- request, scheduler, TTFT, latency, throughput and macOS memory telemetry
- React Studio with Playground, experiments and a unified Runtime Control Center for
  model selection, budgets, scheduling, qualification, memory and managed restart
- reproducible Python benchmark runners with JSON/CSV output

## Run

Requirements:

- Apple Silicon Mac running macOS
- Xcode command-line tools
- CMake ≥3.24 and Ninja
- Go ≥1.25
- Node.js and npm
- Python 3.12

Optional bounded compatibility report for an existing GGUF:

```sh
./scripts/drive-model.sh /absolute/path/model.gguf --model-id local-model
```

To qualify an existing compatible DFlash pair:

```sh
./scripts/drive-model.sh /absolute/path/target.gguf \
  --draft-model /absolute/path/dflash.gguf --model-id local-model
```

This runs an isolated loopback service, stores no prompts or outputs, stops automatically
and writes its private report under ignored `build/pilotllama/`.

Setup fetches exact pinned project dependencies only after consent. It stays inside
the project, never uses `sudo`, and never downloads a model.

```sh
./scripts/setup.sh
./scripts/start.sh --model /absolute/path/model.gguf --model-id local-model
```

Safe defaults are 4,096 context tokens and 512 output tokens. Choose larger
operator budgets only when the model and available unified memory support them:

```sh
./scripts/start.sh --model /absolute/path/model.gguf \
  --context-tokens 8192 --max-output-tokens 1024
```

Optional DFlash acceleration is explicit and uses two existing local files. Queue mode
works after normal pair validation; continuous mode additionally requires exact-pair
qualification with `scripts/test-native-batching.sh`:

```sh
./scripts/start.sh --model /absolute/path/target.gguf \
  --draft-model /absolute/path/dflash.gguf --batching queue

# After the exact pair passes local continuous qualification:
./scripts/test-native-batching.sh /absolute/path/target.gguf \
  /absolute/path/dflash.gguf
./scripts/start.sh --model /absolute/path/target.gguf \
  --draft-model /absolute/path/dflash.gguf --batching continuous
```

Use `--no-draft-model` to run target-only and clear a saved draft selection. Selecting
a different target model no longer carries a previously saved draft forward unless
the operator explicitly supplies `--draft-model` again.

Open `http://127.0.0.1:8080/`. Later starts reuse the saved model selection:

```sh
./scripts/start.sh
./scripts/stop.sh
```

Read-only diagnostics: `./scripts/setup.sh --check`.

## Use from your app

1. Start PilotLlama with an existing local GGUF model.
2. Read the generated local bearer token.
3. Configure an OpenAI-compatible client with:
   - base URL: `http://127.0.0.1:8080/v1`
   - API key: the local bearer token
   - model: your configured PilotLlama model alias
4. Send chat-completion requests and stream responses normally.

```text
Your app → http://127.0.0.1:8080/v1 → PilotLlama → local GGUF on Metal
```

## API

```sh
TOKEN="$(cat build/pilotllama/auth.token)"

curl --no-buffer http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"local-model",
    "messages":[
      {"role":"system","content":"Answer clearly and concisely."},
      {"role":"user","content":"What did we establish earlier about local inference?"},
      {"role":"assistant","content":"We established that the model runs locally on this Mac."},
      {"role":"user","content":"Explain why unified memory matters."}
    ],
    "max_tokens":128,
    "temperature":0.7,
    "top_p":0.9,
    "top_k":40,
    "seed":12345,
    "stop":["END"],
    "stream":true
  }'
```

The API accepts bounded `system`/`user`/`assistant` conversation history when the
loaded GGUF's embedded chat template supports it. Models with detected thinking
support can opt in with `"thinking":true`; reasoning streams separately from final
content. Generation remains greedy with
`temperature:0`, or applications can opt into bounded `temperature`, `top_p`,
`top_k`, `seed`, and `stop` controls. Output is bounded by the operator-selected runtime budget (safe default:
512 tokens). Unsupported capabilities and options fail explicitly. See
[the API contract](docs/public-api.md).

## Request path

![PilotLlama request lifecycle](docs/assets/pilotllama-request-lifecycle.svg)

Go checks authentication → admission → rate → input/public capacity, then opens a
gRPC stream. C++ admits the request into a bounded scheduler, runs llama.cpp, and
streams token bytes and metrics back. Deadlines and disconnects propagate to native
cooperative cancellation. Metal work is not preempted, so cleanup may outlast HTTP.

## Scheduling

| Mode | Native execution | Status |
| --- | --- | --- |
| `queue` | 1 active request + bounded wait queue; optional compatible DFlash draft | default |
| `continuous` | 2 active sequences + 2 waiting; optional exact-pair-qualified DFlash | opt-in; built-in verified or exact locally qualified GGUF/pair |

The measured two-client comparison reduced median TTFT from **1,146.7 ms to
262.3 ms** and native queue wait from **1,012.5 ms to 0.012 ms**. Throughput changed
from **23.07 to 22.04 tok/s**; PilotLlama therefore claims earlier response starts,
not higher throughput. [Method and evidence](docs/benchmarking.md).

## Model Driving Test and compatibility

PilotLlama does not assume that every GGUF supports every chat feature or scheduling
mode. Qualify an existing local artifact without downloading or modifying it:

```sh
./scripts/drive-model.sh /absolute/path/model.gguf --model-id local-model
```

The bounded test records core generation, multi-turn chat, deterministic sampling,
structured JSON and tools where applicable, plus exact-file continuous qualification.
With `--draft-model`, it additionally records the draft artifact name/size, drafted
tokens, accepted tokens and measured acceptance rate.
It stores no prompts or model outputs.

- **Verified:** the complete applicable test suite passed for the exact artifact,
  pinned runtime and recorded configuration.
- **Compatible:** core serving works, but one or more optional checks were not passed
  or do not apply. The tested Bolt Q4 is currently 4/5 and `compatible`.
- **Experimental:** loading or some requested capabilities remain unsupported,
  unqualified or failed; use only with explicit operator review.

## Model boundary

Queue mode accepts decoder-only GGUFs recognized by the pinned llama.cpp when their
embedded chat template is supported. Unknown template-less or unsupported models fail
closed. The verified Nemotron-H shape has a fixed fallback because its GGUF lacks a
template. Continuous mode requires built-in verification or a successful bounded local
qualification tied to the unchanged GGUF file; otherwise it fails closed.

Queue mode has also been verified with an embedded-template Nemotron Bolt GGUF. There
is no claim of arbitrary GGUF, universal target/draft compatibility, full Jinja or
multimodal support. DFlash is optional; continuous DFlash requires an unchanged,
locally qualified target/draft pair. Failed requested initialization fails closed
instead of silently changing execution. Supported templates
can use multi-turn chat, bounded tools and structured JSON.
[Details](docs/model-selection.md).

### Bounded compatibility evidence

| Existing local artifact | Core chat | Multi-turn | Reasoning | Tools | Structured JSON | Continuous |
| --- | --- | --- | --- | --- | --- | --- |
| Nemotron Nano 4B, Q4_K_M | verified JSON/SSE | unavailable with its template-less fallback | unavailable | unavailable | unqualified | built-in verified |
| Nemotron 3.5 Bolt E4B, Q4_K_M | verified; DFlash pair qualified | verified | detected | verified | bounded probe failed | exact file qualified |
| Liquid AI LFM3-1.7B, QAD-Q4_0 | verified | verified | detected | verified | bounded probe failed | exact file qualified |

These are results for the exact local GGUFs and pinned runtime—not promises for every
model, quantization or machine. Bolt passed 4/5 bounded Driving Test checks; its strict
structured-JSON probe failed and remains experimental. Declared context is a model
maximum, while actual service context/output budgets remain operator configured.
The bounded DFlash run measured 119 proposed and 9 accepted draft tokens (7.56%) across
the suite. This validates accounting and compatibility, not a universal speedup; the
earlier controlled Phase 9A comparison measured 28.16 versus 40.59 tok/s with identical
greedy output on this machine.

A bounded two-client continuous comparison completed 4/4 requests in both modes with
identical output digests. DFlash measured 24.49% acceptance, but throughput was 15.96
versus 16.53 tok/s target-only and median TTFT was higher. PilotLlama therefore makes
no general continuous-DFlash speedup claim. [Method and evidence](docs/benchmarking.md#continuous-dflash-comparison).

The exact LFM3 QAD-Q4_0 artifact passed 4/5 Driving Test checks. In a bounded identical
30-token-output comparison on the inspected M4 Air, single-request queue and continuous
throughput were effectively equal (68.29 versus 68.28 tok/s). At concurrency two,
aggregate throughput was 69.37 tok/s in queue mode and 110.24 tok/s in continuous mode;
median latency fell from 848.9 ms to 527.8 ms, and all completed outputs had identical
digests. This is exact-artifact scheduling evidence, not a universal LFM3 performance claim.

## Components

| Directory | Responsibility |
| --- | --- |
| [`studio/`](studio/) | React + TypeScript operations UI |
| [`controlplane/`](controlplane/) | Go HTTP/SSE API, policy, lifecycle and observability |
| [`proto/`](proto/) | Internal gRPC v1 contract |
| [`src/`](src/) | C++ runtime, schedulers, llama.cpp adapter and RPC server |
| [`benchmarks/`](benchmarks/) | Python workloads, runners and analysis |
| [`scripts/`](scripts/) | User-space setup, lifecycle and verification |

Metrics retain their source boundaries: browser, Go, RPC and native timings are not
interchangeable; RSS and llama allocations overlap; P95/P99 remain null below their
20/100-sample thresholds. See [observability](docs/observability.md) and
[memory methodology](docs/memory-observability.md).

## Verify

Phase 8J closeout passed 63/63 development C++ tests; 70 executed/71 gRPC
CTest cases with the cross-language case separately passing; Go race and vet;
22/22 model-free HTTP/gRPC integration cases; 12/12 Python tests; generated-binding
and wire checks; Studio production build; release version/help; and Doctor with
0 failures and 0 warnings. Real-model checks are bounded compatibility evidence,
not stress, thermal, leak, quality or universal performance claims.

```sh
cmake --preset dev && cmake --build --preset dev
ctest --test-dir build/dev --output-on-failure
sh scripts/go-local.sh test -race ./...
sh scripts/go-local.sh vet ./...
.venv/bin/python -m unittest discover -s benchmarks/tests -v
sh scripts/generate-proto.sh --check
sh scripts/test-proto-interop.sh
sh scripts/test-rpc.sh
(cd studio && npm run build)
./scripts/doctor.sh
```

PilotLlama is licensed under [Apache-2.0](LICENSE). Models, dependencies, builds,
raw results, credentials and local state are excluded from the repository. The
PilotLlama mascot is original project artwork; see [NOTICE](NOTICE) and
[third-party notices](THIRD_PARTY_NOTICES.md).

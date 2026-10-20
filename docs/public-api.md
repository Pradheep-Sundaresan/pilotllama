# Phase 3C — local chat-completions API

`POST /v1/chat/completions` now forwards public HTTP requests through the Go gRPC
client to the C++ native server. It implements a deliberately limited Chat
Completions wire-format subset, checked against the official
[create reference](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)
and [streaming events](https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events).
It does not contact OpenAI or require an OpenAI account/API key. No SDK dependency
was installed and no particular SDK version compatibility is claimed.

## Run

Start the existing native server with an already available local GGUF:

```sh
sh scripts/build-native-rpc.sh
./build/grpc/inferno_rpc_server --model /absolute/path/to/local-model.gguf \
  --listen 127.0.0.1:8081 --model-id local-nemotron-4b
```

In another terminal:

```sh
sh scripts/go-local.sh build -o ../build/inferno-api ./cmd/inferno-api
./build/inferno-api --backend-grpc 127.0.0.1:8081 --request-timeout 60s
```

Example request (add `"stream":true` for SSE):

```sh
curl http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"local-nemotron-4b","messages":[{"role":"user","content":"What is 2 + 2? Answer with one digit."}],"max_completion_tokens":1}'
```

Use curl's `-N` for immediate stream display. These commands never download weights
or install dependencies.
Health endpoints remain available. The legacy `--backend` HTTP-readiness mode
retains its prior behavior and has no generation route; select `--backend-grpc`
to enable generation. Explicitly supplying both modes is rejected.

## Optional local authentication — Phase 3E-1

Authentication is disabled by default for existing local clients. Enable it by
passing the **name** of an environment variable, rather than a token on the server
command line. For example, from the project root:

```sh
export INFERNO_AUTH_TOKEN="$(.venv/bin/python -c 'import secrets; print(secrets.token_urlsafe(32))')"
./build/inferno-api --backend-grpc 127.0.0.1:8081 --auth-token-env INFERNO_AUTH_TOKEN
```

Keep the generated secret private and provide the same value to your client. Send
one `Authorization: Bearer TOKEN` header on each generation request. Do not put
credentials in URLs. This is a local Inferno token, not an OpenAI API key; it does
not enable billing. The same flag works with `--backend-exec` managed mode.

An explicitly selected missing/empty variable or invalid token fails startup.
Tokens must be 32..256 ASCII bearer-token characters: letters, digits, `-._~+/`,
and optional trailing `=` padding; the length bound does not guarantee entropy.
Use a randomly generated secret. The CLI removes the chosen variable from its
environment before starting any managed native child. Service instances retain a
SHA-256 digest and compare fixed-size digests with constant-time comparison. The
CLI configuration still holds the secret in process memory; no memory-erasure
guarantee is claimed.

Missing, malformed, duplicate or incorrect authorization returns HTTP 401 with a
fixed JSON `authentication_error`/`invalid_api_key` envelope and
`WWW-Authenticate: Bearer realm="inferno"`. Scheme matching is case-insensitive;
the token is case-sensitive. Credentials are accepted only through that header,
never query parameters or request-body fields. Rejection precedes application body
parsing, request-ID allocation, admission and backend calls. It never starts SSE.
The service does not log or echo tokens or forward authorization to the C++ RPC.

Health/readiness stay public; legacy mode still has no generation route. This adds
one optional shared secret, without users, roles, rotation endpoints,
TLS or remote access. All listeners remain loopback-only.

Verification: model-free tests cover rejection before backend/body/admission work,
valid JSON/SSE, public health, disabled-auth compatibility and invalid configuration.
The real C++ fixture also verifies missing/wrong/valid credentials through live
HTTP and gRPC for both response modes. No model download or native rerun is needed
for this HTTP access-control change.

## Optional request-rate policy — Phase 3E-2

Use `--rate-per-minute 60 --rate-burst 4` to allow a four-request initial burst
and refill at one request per second. Rate zero (default) disables the limiter;
when enabled, omitted/zero burst selects one. Rate accepts integers 1..60000 and
burst accepts 1..1024. Supplying a positive burst with a disabled rate is invalid.
These flags work with managed or external gRPC backends and optional authentication.

The policy is one process-local token bucket shared by **all generation clients**,
not a per-IP or per-user quota. It starts full, refills continuously using elapsed
Go clock time, and caps accumulated credit at the configured burst. One accepted
middleware check consumes one credit. There is no waiting queue or timer worker;
restart resets the bucket. Tests use controlled timestamps; production uses Go's
monotonic clock component. This policy exposes no new measurement or metric.

Authentication runs first. Unauthenticated requests consume no credit. Rate
rejection happens before application body parsing, request-ID allocation, the
existing four-handler capacity check, or backend calls. It returns HTTP 429 with
JSON `rate_limit_error`/`rate_limit_exceeded` and `Retry-After` whole seconds,
rounded up until one credit refills. That delay is advisory: concurrent clients can
consume the next credit. Rejected streaming requests do not start SSE.

Once admitted by the rate policy, requests consume credit even if subsequent
validation, capacity checks, cancellation or inference fails; credits are not
refunded. Existing concurrency/queue limits still apply and can independently
return 429 with their existing capacity-error codes. Health/readiness are exempt.
Legacy HTTP probe mode still has no generation route. No distributed quota,
per-identity accounting, token/output-rate limit or rate-control endpoint is added.

Verification covers burst/refill/capacity, rounded retry delays, concurrent callers,
auth ordering, backend/admission exclusion, public health, configuration bounds and
disabled defaults. A live model-free C++ fixture verifies successful HTTP generation
followed by JSON/SSE rate rejections; native inference is unchanged.

## Model discovery — Phase 3F-1

In gRPC mode, `GET /v1/models` lists the one live READY backend alias;
`GET /v1/models/{model}` retrieves that alias. Both require the same bearer token
when authentication is enabled. Discovery does not consume generation rate credit.
Legacy HTTP probe mode exposes neither route. Unknown aliases return 404
`model_not_found`; a non-ready backend returns 503, and transport failures use the
same sanitized mapping as generation. There is no stale catalog cache or disk scan.

The response follows the [list](https://developers.openai.com/api/reference/resources/models/methods/list)
and [retrieve](https://developers.openai.com/api/reference/resources/models/methods/retrieve)
envelope conventions, with this deliberate limitation: `created` is **null**, since
gRPC supplies no artifact creation timestamp. Official OpenAI model objects specify
an integer there, so this is a documented compatibility deviation, not full schema
or SDK compatibility. `owned_by: "inferno"` labels the local serving catalog, not the
weight author or license holder. The ID is the caller-supplied alias, not a hash.

Example list response:

```json
{"object":"list","data":[{"id":"local-nemotron-4b","object":"model","created":null,"owned_by":"inferno"}]}
```

Generation and discovery use the same live readiness/alias resolver. A successful
lookup is a point-in-time observation, not a reservation or readiness guarantee for
the next request. No alias rewriting, fallback, multiple backends, downloads,
load/unload controls or model deletion is implemented.

Discovery and generation share four active public-operation slots, without another
queue. Discovery overload returns 429 `public_capacity_exceeded`. Real status calls
retain their one-second parent-bounded gRPC budget; health remains independent.
Model-free tests verify authentication, ready/non-ready states, rate independence,
capacity, list/detail consistency and no generation side effects. A real C++ fixture
also verifies discovery followed by generation using the returned alias.

## Supported request subset

| Field | Support |
| --- | --- |
| `model` | Required string matching the READY backend's supplied model alias |
| `messages` | Exactly one object with role `user` and string content; existing fixed single-turn native template applies |
| `stream` | Boolean, default false |
| `max_completion_tokens` or `max_tokens` | One or the other; integer 1–512; default 128 |
| `temperature` | Omitted or 0; generation remains greedy |
| `n` | Omitted or 1 |

Unknown fields are rejected rather than ignored. System/developer messages, chat
history, tools, multimodal content, stop sequences, sampling controls, logprobs,
response schemas, stream_options/usage chunks and OpenAI model-deletion APIs are not
supported. The model alias is not a verified artifact hash. Authorization headers
are not authentication yet; the service remains loopback-only with no remote mode.
Infrastructure authentication/rate policy remains another approved milestone.

Requests require application/json, a body of at most 128 KiB and a prompt of at most
65536 UTF-8 bytes without template control delimiters. The native context limit
still applies after tokenization; a native context-budget failure currently returns
a sanitized backend error, not an invented prompt-token count. Malformed JSON,
unknown fields and unsupported values fail before generation. Go's standard JSON
string decoding semantics apply to escapes; raw non-UTF-8 bodies are rejected.

## Responses and streaming

Regular success returns id/object/created/model and one assistant-message choice.
Stream success returns `data:` JSON records with object chat.completion.chunk:
one assistant-role delta, content deltas, one final empty delta with finish_reason,
then `data: [DONE]`. IDs, model and creation timestamp stay consistent. Public IDs
are chatcmpl- plus the internal decimal request ID; X-Request-ID carries that ID.
IDs use a per-service cryptographically seeded atomic sequence, reducing restart
collisions with requests still cleaning up in a backend; this is not a distributed
ID allocator. Incoming client IDs are not trusted/forwarded as native IDs.

finish_reason is `length` when measured native output count reaches the requested
budget; otherwise a completed generation reports `stop`. It is based on native
token events, including empty pieces, not text bytes or number of public chunks.
No prompt token counts, usage totals, model fingerprints or performance fields are
fabricated. Usage is omitted because prompt-token counts are not exposed yet.
Native/RPC observations remain internal; no public metrics endpoint is added.

Raw native pieces can split Unicode characters. The HTTP adapter buffers incomplete
code points (at most three pending bytes between pieces), emits valid UTF-8 JSON,
and counts native tokens independently. Empty pieces need not create empty public
content chunks. Invalid bytes or an incomplete final code point fail explicitly;
there is no silent replacement of generated text.

SSE headers are committed only at the first valid text or successful zero-output
completion. Before commitment, errors return JSON with an appropriate HTTP status.
After commitment, errors use a `data:` error envelope and end the stream, without
a success finish or [DONE]. A broken writer may receive no further error record.
Already emitted text remains partial; clients must require the successful terminal
sequence rather than interpreting EOF as success. This error behavior is Inferno's
explicit subset policy, not a claim of compatibility with every streaming client.

| Condition | HTTP status before streaming |
| --- | --- |
| Malformed/unsupported request or prompt | 400 |
| Unknown/not-loaded requested alias on a READY backend | 404 |
| Cancellation while response remains writable | 408 |
| Body exceeds cap / wrong media type | 413 / 415 |
| HTTP handler or native admission full | 429 server_busy |
| Native/protocol failure, invalid text, oversized output | 502 |
| Backend unreachable or not ready | 503 |
| Request/RPC/native deadline | 504 |

Errors have an error object with message/type/param/code. Messages are fixed and
sanitized, never forwarded native paths/prompts. Native failed/cancelled/timed-out
Finished events are never returned as successful completions. A queue-full 429 is
bounded admission, not a claim that time-based rate limiting exists.

## Bounds, backpressure and shutdown

[Authenticated request history](request-observability.md) exposes measured per-request
observations separately from the compatible JSON/SSE envelopes. It never retains prompts
or generated text and is limited to the latest 128 attempts that reached Generate.
[Aggregate metrics](aggregate-observability.md) summarize the retained completion
window with sample counts, qualified percentiles and completion-attributed token rates.
Authenticated `GET /internal/v1/memory` exposes the separate bounded native
[memory snapshot](memory-observability.md). It reports macOS process accounting and
llama.cpp allocations without adding them together. It is not part of the public
OpenAI-compatible API and does not populate the completion-window memory field.

Authenticated [admission drain/resume controls](admission-controls.md) can pause new
generation without cancelling existing handlers or unloading the model. They report
Go handler admission state, not native idle; readiness is unavailable while drained.

There are at most four active HTTP generation/discovery handlers combined; excess requests are rejected
immediately, with no extra inference queue in Go. Output raw bytes are capped at
1 MiB per request for both response modes. Regular responses buffer bounded text;
SSE writes synchronously and flushes records. Write/flush failure returns from the
consumer, cancels the child RPC and stops further output.

The default request budget is 60 seconds, configurable up to one hour (Config zero
selects the default), starting at handler admission and shared with backend status
and generation. Parent context deadlines take precedence. Request disconnects and
HTTP shutdown propagate cancellation through gRPC. Native Metal cleanup can outlast
the HTTP deadline; no preemption or cleanup-time guarantee is introduced.

The server's global five-second WriteTimeout was replaced by per-write deadlines:
JSON health/error writes and every SSE write/flush have a five-second deadline,
while a long prefill can wait within its request budget. Read-header/body-read/idle limits
remain 2/5/30 seconds; the body deadline is capped by the earlier request budget
and cleared after input to permit long prefill. The header limit is 16 KiB plus
Go's parser allowance; shutdown grace is
five seconds. Base-context cancellation ends active HTTP/RPC work; the server loop
is joined. Standalone Handler embeddings need writers supporting ResponseController
deadlines (Run does); in-memory recorders have no socket deadline to enforce.
These bounds do not establish process RSS limits or bound all TCP connections.

## Verification — 2026-10-17

- Offline race-enabled Go tests cover response/chunk shape and IDs, split/empty/raw
  Unicode pieces, malformed/unsupported requests, body/output limits, admission,
  deadlines, cancellation, writer errors and partial-stream failure. Legacy health,
  readiness and configuration tests still pass. Go vet/build pass.
- Eleven model-free real-localhost integration tests include public JSON/SSE,
  sanitized pre-/post-stream errors, HTTP timeout/disconnect recovery and active
  SSE shutdown while native cleanup is still blocked. Go HTTP shutdown joins without
  waiting for the gated backend; releasing it then permits C++ cleanup/shutdown.
- Seven native subcases pass with the existing cached Nemotron model; the new public
  HTTP JSON and SSE paths both return the real arithmetic output `4` through
  HTTP -> Go -> gRPC -> C++ -> llama.cpp/Metal. Native stop/recovery tests still pass.

Commands: `sh scripts/go-local.sh test -race ./...`, `sh scripts/go-local.sh vet ./...`,
`sh scripts/test-rpc.sh`, and `sh scripts/test-native-rpc.sh EXISTING_LOCAL_GGUF`.
The latter two require localhost/Metal sandbox access. Logs: ignored build/phase-3c-*.log.
No dependency/model downloads, system changes, benchmark, performance improvement,
new C++ sanitizer/race-proof claim, dashboard or model manager are included.

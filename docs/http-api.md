# PilotLlama HTTP API

This is the application-facing contract for the current loopback service. PilotLlama
implements a documented subset of OpenAI Chat Completions; it does not claim complete
OpenAI API or SDK compatibility. See the [compatibility matrix](api-compatibility.md)
and machine-readable [`openapi.yaml`](../api/openapi.yaml).

## Connect

Native applications on the same Mac use:

```text
base URL  http://127.0.0.1:8080/v1
API key   build/pilotllama/auth.token
model     the id returned by GET /v1/models
```

Keep the token in server-side configuration. Never embed it in browser JavaScript,
commit it, send it in a URL, or expose port 8080 publicly.

```sh
TOKEN="$(cat build/pilotllama/auth.token)"
curl -sS http://127.0.0.1:8080/v1/models \
  -H "Authorization: Bearer $TOKEN"
```

`GET /healthz` is unauthenticated liveness and returns
`{"status":"ok","service":"inferno-controlplane"}`. `GET /readyz` is
unauthenticated readiness: `200 {"ready":true}` or `503` with reason
`backend_unavailable` or `admission_draining`.

Model discovery is authenticated. `GET /v1/models` returns the one currently READY
alias:

```json
{
  "object": "list",
  "data": [{"id":"local-model","object":"model","created":null,"owned_by":"inferno"}]
}
```

The alias is operator-supplied, not an artifact hash or model registry entry.

## Stream chat

The generation route is `POST /v1/chat/completions`.

```sh
curl -N http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"local-model",
    "messages":[
      {"role":"system","content":"Answer clearly."},
      {"role":"user","content":"Explain continuous batching simply."}
    ],
    "max_tokens":256,
    "temperature":0.7,
    "top_p":0.9,
    "stream":true,
    "pilotllama":{"include_metrics":true}
  }'
```

Success is `text/event-stream`. The response also carries `X-Request-ID`; a request
admitted to the Go waiting lobby may carry a point-in-time `X-Queue-Position`.

The successful event order is:

```text
data: {"object":"chat.completion.chunk",...,"choices":[{"delta":{"role":"assistant","content":""},...}]}
data: {"object":"chat.completion.chunk",...,"choices":[{"delta":{"content":"Hello"},...}]}
data: {"object":"chat.completion.chunk",...,"choices":[{"delta":{},"finish_reason":"stop",...}]}
data: {"object":"pilotllama.metrics",...,"metrics":{...}}  # only when requested
data: [DONE]
```

Thinking-capable qualified models can receive `"thinking":true`. Reasoning then
arrives only in `choices[0].delta.reasoning_content`; applications must keep it
separate from `delta.content`.

The optional namespaced metrics receipt contains only measurements available for
that completed request. Its current shape is:

```json
{
  "id": "chatcmpl-123",
  "object": "pilotllama.metrics",
  "created": 1794096000,
  "model": "local-model",
  "request_id": "123",
  "metrics": {
    "output_tokens": 42,
    "go_queue_wait_ms": 0.01,
    "rpc_latency_ms": 790.2,
    "rpc_ttft_ms": 96.1,
    "native": {
      "runtime_latency_ms": 789.8,
      "queue_wait_ms": 0.0,
      "prefill_phase_ms": 95.8,
      "decode_phase_ms": 694.0,
      "ttft_ms": 96.0,
      "decode_tokens_per_second": 60.5,
      "speculative_drafted_tokens": null,
      "speculative_accepted_tokens": null,
      "speculative_acceptance_rate": null
    }
  }
}
```

Numbers above illustrate the schema, not a benchmark. `null` means unavailable; it
must never be converted to zero. The receipt is a PilotLlama extension, not OpenAI
`usage`. Prompt-token usage is not measured end to end, so `stream_options` remains
unsupported rather than fabricating totals. Without `pilotllama.include_metrics`,
the pre-existing OpenAI-style stream is unchanged. The option is stream-only.

Finish reasons are `stop`, `length`, or `tool_calls`. A client must require the
finish event followed by `[DONE]`; EOF alone is not success.

## Supported request controls

| Field | Current contract |
| --- | --- |
| `messages` | 1–64 bounded system/user/assistant/tool messages; capability-gated by the loaded template |
| `max_tokens` / `max_completion_tokens` | Use one; 1 through the operator-configured output cap; request default 128 |
| `temperature` | 0–2; default 0 |
| `top_p` | greater than 0 through 1; default 1 |
| `top_k` | 0–100000; default 0 |
| `seed` | optional uint32 |
| `stop` | one string or up to four bounded strings |
| `n` | omitted or 1 |
| `thinking` | optional capability-gated PilotLlama extension |
| `pilotllama.include_metrics` | optional stream-only measured receipt |
| `tools`, `tool_choice` | bounded function tools; PilotLlama never executes them |
| `response_format` | text, `json_object`, or bounded strict `json_schema`; mutually exclusive with tools |

Unknown fields fail with `400 unsupported_request` rather than being ignored.

## Cancellation and overload

Abort the HTTP request when the user presses Stop or disconnects. Go cancels its
gRPC stream; C++ cooperatively stops native work and releases scheduler capacity
after cleanup. A cancellation while still in the Go lobby never opens a native RPC.

Errors use:

```json
{"error":{"message":"...","type":"...","param":null,"code":"..."}}
```

| Status | Important codes | Client action |
| --- | --- | --- |
| 401 | `invalid_api_key` | Fix server-side configuration; never retry with the same bad token |
| 429 | `queue_full`, `rate_limit_exceeded`, `server_busy` | Honor `Retry-After` when present; otherwise bounded backoff |
| 503 | `backend_unavailable`, `admission_draining` | Show temporarily unavailable and retry deliberately |
| 504 | `queue_wait_timeout`, `request_timeout` | Report timeout; do not assume generation completed |

After SSE has started, an error is delivered as a `data:` error envelope. It has no
success finish and no `[DONE]`; already received text is partial.

## Networking boundary

PilotLlama deliberately binds only to explicit IPv4 loopback. A native PicoChat-like
backend on the same Mac should use `127.0.0.1:8080`. A normal Docker Desktop container
cannot treat its own `localhost` as the Mac host, and `host.docker.internal` is not a
qualified path to PilotLlama's loopback-only listener. Do not work around this by
binding PilotLlama to `0.0.0.0`. Run the inference-calling application backend
natively until a separately secured container bridge is designed and verified.

## Examples

- [TypeScript streaming client](../examples/typescript/pilotllama-stream.ts)
- [Python streaming client](../examples/python/pilotllama_stream.py)
- [Full historical subset and bounds](public-api.md)

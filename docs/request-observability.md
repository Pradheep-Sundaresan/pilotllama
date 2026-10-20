# Per-request observability — Phase 3H-1

In gRPC mode, authenticated `GET /internal/v1/requests` returns `capacity:128` and
`records`, newest completion first. `GET /internal/v1/requests/{request_id}` returns
one retained record, or 404 `request_not_found` if absent/evicted. Use the generation
response's `X-Request-ID`; IDs are JSON strings to preserve uint64 precision in browsers.
Authentication must be configured; otherwise these endpoints return 403. Wrong/missing
credentials return 401 when configured. Health stays public. Legacy mode has no history API.

The fixed 128-entry in-memory ring overwrites the oldest entry on completion and
resets with the Go service. It retains only attempts that invoked the Go gRPC Generate
method, including its failures; pre-RPC authentication, drain, rate, capacity, body,
validation and readiness rejections are excluded. It contains no in-flight records
or per-token timeline and is not an audit log. It survives backend stop/start within
the same Go service. No prompts, output text, authorization, model paths or raw error
messages are retained. No disk writes, dependencies, background workers or stream
buffers are added. Snapshot output is bounded by 128 records.

## Fields and methodology

| Field | Source, unit and meaning |
| --- | --- |
| `request_id` | Go generation handler's allocated uint64, encoded as a decimal string; correlates the HTTP header and native request |
| `finished_at_unix_ms` | Go wall clock at record construction, Unix milliseconds; ordering follows insertion/completion, not necessarily wall-clock order |
| `outcome` | `completed` when the public handler validates complete transport, completed native state and valid generated text; `failed` otherwise; `delivery_failed` overrides when an SSE write/flush failed |
| `handler_latency_ms` | Measured Go monotonic elapsed milliseconds from chat-handler entry, after auth/drain/rate middleware, to deferred record construction; includes body/status/RPC work and final response-write attempts, excludes model load and prior middleware |
| `rpc_latency_ms` | Measured Go Generate invocation clock, immediately before opening the RPC through method return, including synchronous consumer/backpressure; null if no RPC opened |
| `rpc_ttft_ms` | Same RPC start through first validated received native token event, including an empty piece; milliseconds, null without a token |
| `received_tokens` | Number of validated native token events received by Go; includes empty pieces, excludes EOG; can be a partial count |
| `token_bytes` | Sum of raw received token-piece lengths in bytes; not HTTP or network-wire bytes |
| `receive_tokens_per_second` | Derived `received_tokens / (rpc_latency_ms / 1000)` for positive count/duration, otherwise null; per-call receive rate, not native decode speed or aggregate throughput |
| `rpc_code` | Final gRPC status observed by the client, as a string; null if no final transport status was observed |
| `transport_complete` | Go protocol validator observed valid Finished and terminal OK EOF; not sufficient by itself for native success |
| `native_state` | Native Finished terminal-state enum, or null without a validated Finished; no inferred intermediate lifecycle states |
| `native` | Copied native Finished measurements, or null if absent; individual unavailable values stay null |
| `stream_write_failed` | Observed SSE write/flush failure flag; null for non-stream responses; false does not prove the remote client consumed the stream |

`native` contains `output_tokens`, `runtime_latency_ms`, `queue_wait_ms`,
`backend_execution_ms`, `prefill_phase_ms`, `decode_phase_ms`, `ttft_ms`,
`mean_inter_token_ms` and `decode_tokens_per_second`. Their C++ sources, phase
boundaries, count rules, units and formulas are unchanged and documented in
[native observability](observability.md). Missing values are null, not zeros. Native
TTFT excludes model load and includes runtime queueing; RPC TTFT is measured from
Go's RPC opening. Neither is the remote HTTP client's TTFT.

The compatible completion response stays unchanged. JSON writes currently do not
report delivery errors to this instrumentation; `completed` describes validation,
not successful client delivery. Even native completed + transport OK can coexist
with failed public delivery or invalid UTF-8; inspect the separate fields. Partial
or failed-request rates must not be presented as successful throughput. No aggregate
rate, percentiles, queue-depth sampling, memory or performance improvement is claimed.

## Aggregate summaries

Phase 3H-2 adds [bounded, windowed aggregate summaries](aggregate-observability.md)
using the retained measurements, without changing the per-request contract.

## Verification

Model-free tests cover successful/failed capture, null preservation, no pre-RPC
capture, authentication, missing IDs, copied measurements, privacy, concurrent
read/write and deterministic oldest-entry eviction. Existing live HTTP/gRPC fixture
tests verify two successful JSON/SSE records with matching native/received counts,
byte counts, native runtime latency and RPC latency. Fixture timings are real test
observations, not model benchmarks. Native inference execution was unchanged and
was not rerun for this milestone.

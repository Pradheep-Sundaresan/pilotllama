# Internal gRPC contract — Phase 3B-1

Status: Phase 3B-1 defined schema and architecture. Phase 3B-2 adds local tools,
generated bindings and serialization checks.
Phase 3B-3 implements the Runtime-backed RPC adapter and Go per-call observations,
verified with a model-free fixture. Phase 3B-4 adds verified native model wiring; Phase 3C implements the public Chat Completions subset; see
[the API contract](public-api.md). The native server is an explicit local process;
adapter limits are stated throughout this contract.
The source of truth is [inference.proto](../proto/inferno/v1/inference.proto).
The dependency/generation plan is [grpc-dependencies.md](grpc-dependencies.md).
The Phase 3A service, tests, HTTP health/readiness and shutdown remain unchanged.
Its HTTP backend probe remains a compatibility mode; opt-in gRPC readiness uses
the implemented GetStatus client. Internal generation uses gRPC only.

```text
Client / future Studio
  -> Go: public HTTP/REST, documented chat completions subset and SSE
  -> C++: internal gRPC/Protocol Buffers, server-streaming Generate (adapter implemented)
  -> existing C++ Runtime -> llama.cpp -> Metal / CPU
```

There will be no internal generation HTTP/SSE server or gRPC-to-HTTP gateway.
Go owns public validation, authentication/rate limits, routing and model lifecycle
coordination. C++ owns bounded admission, execution, cooperative stops and runtime
measurements. There is one inference queue, owned by C++; Go must not duplicate it.
No Kubernetes, service mesh, reflection service, xDS configuration, telemetry agent
or automatic discovery is needed.

## Service and compatibility

Package `inferno.v1`; service `InferenceService`. Protobuf field numbers and enum
values are stable once bindings are released: never reuse removed numbers; reserve
them instead. Zero enum values are unspecified and must not be treated as success.
Reject missing event payloads, unexpected ordering, invalid IDs and invalid required
semantic fields. Explicitly map native request states and errors: protobuf enum
numbers do not match the C++ enum ordinals. Unknown added fields are tolerated; new incompatible semantics
require a new package version. Protocol validation belongs in handlers/clients,
not in generated code. Proto3 `optional` preserves unavailable numeric metrics.

| RPC | Contract |
| --- | --- |
| `GetStatus` | Unary. A successful response proves process liveness. Only `BACKEND_STATE_READY` means model/runtime readiness. Other states: no model, loading, draining, failed. Optional model ID is a caller-supplied alias, not a verified artifact hash or filesystem path. It includes a point-in-time C++ scheduler snapshot and may include Phase 5B's bounded memory snapshot. Queue-full does not make a loaded runtime unready. No model load/download is triggered. |
| `Generate` | Server-streaming. Nonzero uint64 ID unique among active requests, UTF-8 prompt, explicit output limit 1–512. Use existing native prompt validation (65536-byte input cap, no embedded template delimiters, context-budget check). Deadline lives in the gRPC context. |
| `Cancel` | Unary. Nonzero ID. Returns requested, already requested, or finished while the request is retained. Unknown/retired ID returns NOT_FOUND. Intent is not completion; queued items retain their slots until dequeue. |

The initial backend and Go dial target must use explicit loopback IPs only, with
no proxy, DNS discovery, wildcard binding or remote target. One reusable Go channel
serves the backend; do not reconnect for each request. Insecure credentials are
allowed only for this local mode. Loopback is not authentication against other
local processes. A future Mac mini target requires explicit remote configuration,
TLS/server identity and authentication approval; the proto itself is host-independent.
No remote exposure is authorized now.

## Stream lifecycle, ownership and bounds

For an admitted request, send `accepted`, zero or more `token` events, exactly one
`finished` event, then OK trailers. All events carry the same request ID. Accepted
means queued/admitted ownership, not that GPU execution has begun. Immediate EOG
may produce zero token events. One token record represents exactly one native
non-EOG event, including empty pieces; `output_tokens` starts at 1 and increases
by exactly 1. `Finished.metrics.output_tokens` must equal the emitted native event
count for a completed stream. `Finished` has a nonzero terminal state,
metrics (including measured runtime latency), and an error for non-completed states.
For non-completed states, native counts can exceed delivered records if a callback
failed after the runtime counted an event; retain the measured count rather than
fabricating a missing token. Final text is not repeated. Partial bytes cannot be
retracted after failure.

Token pieces use protobuf `bytes`: a native token can split UTF-8. Go must join
incomplete code points when producing valid public JSON/SSE text. It must retain
the native token count independently of the number of public text chunks. Invalid
UTF-8 at stream completion is an explicit public stream error, not silent replacement.

Initial implementation limits: 128 KiB serialized protobuf message size, 64 KiB
maximum token piece, and at most 64 KiB of pending token payload per request (a
single oversized piece fails explicitly). Error messages are bounded to 2 KiB of
UTF-8 and must not include prompts, secrets or local paths. Configure equivalent
client/server limits; the adapter/client now enforce message/piece bounds. The adapter permits at most
64 configured active requests and holds one outstanding write, rather than a token queue.
RPC admission is bounded by configuration, separately from native queue capacity.
Library buffers, native result text and process memory are separate from bridge bounds.

The adapter uses bounded backpressure between the native synchronous callback and RPC writes.
Never build an unbounded stream queue, wait while holding runtime locks, or let a
callback borrow an RPC handler's stack after it returns. A bounded stream bridge
and cancellation handle must remain owned until native completion, even if the
RPC ends early. Disconnect/write failure must wake blocked producers and request
cooperative stop. The adapter uses the inspected pinned callback API for write/cancel/shutdown
reactions and joins one bounded waiter per admitted request in OnDone. No detached workers or busy polling.
A slow consumer must have a finite deadline and cause stop on failed/expired writes;
backpressure can affect native phase timings and must be documented in results.

## Deadlines and cancellation

Go creates the outgoing RPC context from the HTTP request context and the earlier
of its existing deadline and configured generation budget. Every generation RPC
must have a finite deadline, capped at one hour; the public default is a later
implementation/configuration decision. Health probes retain a one-second total
budget. Do not reset budgets on retries; automatic generation retries are disabled.

Use gRPC's deadline propagation, not a payload wall-clock timestamp. At C++ entry,
convert the remaining gRPC budget into a `steady_clock` deadline once, with checked
bounds, before runtime submission. Expired calls do not enqueue. Never transmit
native monotonic timestamps across processes. Go must explicitly derive a context
from the incoming HTTP request; HTTP does not automatically create a gRPC context.
See the official [deadline guide](https://grpc.io/docs/guides/deadlines/).

HTTP disconnect/cancel cancels the outgoing RPC. The C++ adapter must observe RPC
cancellation even while waiting for queued completion or blocked stream writes,
and call the existing request cancellation handle; a native deadline remains a
native deadline rather than being relabeled successful completion. Explicit Cancel
uses the same handle. Cancellation must not depend on another token arriving.
The transport cannot interrupt native Metal work: GPU cleanup can outlast the RPC
budget. The RPC's final status is what Go observed; a server-side completed result
is a separate observation. See the official
[cancellation guide](https://grpc.io/docs/guides/cancellation/) and existing
the cooperative stop limits stated in this document.

Shutdown migration must keep public health behavior, stop new admission, mark the
backend draining, coordinate active handles, and join owned work. The existing Go
five-second HTTP shutdown does not prove that C++ cleanup has finished. Preserve
Phase 3A behavior while designing streaming-specific shutdown, rather than blindly
reusing its five-second write timeout for long generation streams.

## Structured errors

Before accepted, failures return non-OK gRPC status and, when the server can produce
it, an `inferno-error-bin` trailer containing serialized `InferenceError` (max 4 KiB).
This is a custom binary trailer, not `google.rpc.Status` or `grpc-status-details-bin`;
it avoids another proto dependency. ID is the validated request ID, or zero if none
can be extracted. The same error type appears inside non-completed Finished events.

| Condition / ErrorCode | gRPC status before accepted |
| --- | --- |
| Invalid request / INVALID_REQUEST | INVALID_ARGUMENT |
| Duplicate active ID / DUPLICATE_REQUEST_ID | ALREADY_EXISTS |
| Native queue or transport admission full / QUEUE_FULL | RESOURCE_EXHAUSTED |
| Model absent, backend draining / BACKEND_UNAVAILABLE | UNAVAILABLE |
| Unknown Cancel ID / UNKNOWN_REQUEST_ID | NOT_FOUND |
| Cancellation / CANCELLED | CANCELLED |
| Expired deadline / DEADLINE_EXCEEDED | DEADLINE_EXCEEDED |
| Backend/adapter failure / BACKEND_FAILURE | INTERNAL |

After accepted, a runtime failure uses Finished with its terminal state/error and
OK trailers if delivery remains possible. A broken transport or expired/cancelled
RPC may end non-OK at any point with no Finished/error trailer. Such a stream is
incomplete; do not invent native state, metrics or successful output. Even a received
Finished followed by non-OK transport termination is not an RPC success. A missing
Finished with OK trailers is a protocol error. Empty/default codes are invalid.
Phase 3C verifies public error and finish-reason mapping; runtime fields must not be inserted into incompatible public SSE envelopes.

## Measurements to implement with the adapters

The Go client returns per-call observations; there is no public metrics endpoint
yet. Use process-local monotonic clocks. Runtime
fields map exactly to [existing definitions](observability.md); absent protobuf
optionals become null/unavailable, never zero. Metrics must be finite and nonnegative.

| Metric | Unit and source | Methodology |
| --- | --- | --- |
| `rpc_latency_ms` | ms; Go monotonic clock | Immediately before opening Generate through method exit after final receive/consumer and context cleanup; includes channel readiness, network and streaming/consumer time. Record final status and completion flag. |
| `rpc_ttft_ms` | ms; Go monotonic clock | Same start to first validated received Token, including empty pieces. Absent if no token received. Distinct from native TTFT and visible-character latency. |
| `rpc_received_tokens` | events; Go stream decoder | Count validated received Token records, excluding EOG; preserve partial count on interruption. Never infer generation count from UTF-8 bytes. |
| `rpc_token_bytes` | bytes; Go stream decoder | Sum raw `piece` lengths for validated received Token records; empty pieces add zero bytes and one token. Not wire bytes. |
| `rpc_receive_tokens_per_second` | events/s; derived in Go | Received token count divided by positive total RPC duration. Absent for zero count/duration; annotate interrupted streams and do not aggregate them as successful throughput. Includes time before first token. |
| `status_rpc_latency_ms` | ms; Go monotonic clock | Go GetStatus method entry through response validation/failure return; report status separately. |

The scheduler values in `GetStatus` are coherent C++ admission-lock request counts,
not a time series or queue-depth aggregate. See [scheduler methodology](scheduler-observability.md).

The optional memory value contains at most five latest native lifecycle samples and
32 llama.cpp allocation rows. macOS process accounting overlaps llama allocations;
never add them. Buffer rows are not separate RAM/VRAM on Apple unified memory. See
the [memory contract](memory-observability.md).

Do not subtract native duration from RPC duration and call it network latency:
measurement boundaries differ. The Phase 5B memory snapshot is not a completion-window
aggregate. Future aggregation needs windows, sample counts,
completion filters and reproducible workload metadata. No performance gain is claimed.

## Required verification in later milestones

Phase 3B-2 covers binding compilation/round trips and two-way wire interoperability.
Phase 3B-3 adds model-free localhost streams, readiness, admission failure, duplicate
IDs, protocol validation, queued/active cancellation, deadlines, disconnect/slow-reader
recovery and joined shutdown. Preserve all Phase 3A tests; migrate only its internal readiness client
when gRPC readiness is available. Add opt-in cached-model stop/recovery checks only
when native transport works. Phase 3B-1 does not claim any of those transport tests.

Phase 3B-1 verification (2026-10-17): all nine metric names match the existing C++
RequestMetrics structure; duration/rate fields preserve optional presence. Local
documentation links pass. Unchanged Phase 3A Go race-enabled tests pass from cache.
No runtime source, module requirements, CMake targets or shutdown behavior changed;
no C++ build/model run was needed. Protobuf compilation remains unverified because
the generators are not installed.

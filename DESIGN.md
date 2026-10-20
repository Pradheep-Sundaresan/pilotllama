# Inferno design

## Status and boundaries

This document describes the intended design. Inferno implements a generation CLI,
request lifecycle types, a bounded FIFO, and a single joined queue-only worker through
a synchronous backend interface. Phases 6A–6B also provide an explicit two-sequence
batch engine and an opt-in native llama.cpp/Metal executor. Queue mode remains default.
The staged delivery plan and additional product requirements are in
[the roadmap](docs/roadmap.md).
Phase 3 closeout and current single-backend contracts are summarized in
[the completion report](docs/phase-3-completion.md), including authentication/rate
policy, discovery, managed stop/start and bounded request/aggregate observability.
Phase 3A implements Go health/readiness and graceful HTTP shutdown; the
[control-plane guide](docs/control-plane.md) documents those implemented probes.
Phase 3B-1 replaces the planned internal HTTP/SSE transport with
[gRPC/Protocol Buffers](docs/grpc-contract.md). Phase 3B-2 adds pinned local tooling
and generated bindings. Phase 3B-3 implements Runtime-backed RPC handlers and a
measured Go client, verified with a model-free fixture. Phase 3B-4 wires the existing native model backend to a standalone RPC server and
verifies real inference/stop recovery. Phase 3C adds the public HTTP Chat Completions subset; see [API contract](docs/public-api.md).
See [native RPC setup and limitations](docs/phase-3b4.md).
See [adapter ownership and verification](docs/phase-3b3.md).

```text
Client / future Studio
        | public HTTP/REST + SSE (documented Chat Completions subset)
Go control plane
        | localhost gRPC / Protocol Buffers (adapter implemented)
C++ inference core
        | native C/C++ API
llama.cpp -> Metal / CPU -> model

Python benchmark clients measure the public API and upstream baselines separately.
```

Go owns infrastructure policy: validation, authentication, client rate limits,
routing, and coordinated model lifecycle. C++ owns admission and execution-level
policy. Model unload must wait for active users to drain or cancel safely.
llama.cpp supplies inference, graph execution, and cache/batching mechanisms.
The native CLI exposes per-request runtime instrumentation; the Go gRPC client
adds separate per-call transport observations. Phases 3H-1/3H-2 expose bounded
request history and completion-window aggregates; see
[measurement methods and limits](docs/aggregate-observability.md). Queue depth and
memory remain unavailable. Upstream CLI and bench establish separate baselines.

## Request and concurrency plan

The implemented baseline uses a bounded FIFO and one inference worker. Concurrent
admission does not imply concurrent GPU execution, and Go does not duplicate the
native queue. Phase 4E-1 completed the required upstream inspection and documents a
[bounded continuous-batching design](docs/phase-4e-continuous-batching.md).

The Phase 6A model-free engine keeps two active sequence slots in
front of the existing two-item waiting queue. A single joined engine thread owns
llama context mutation. Each decode cycle can place one next token from each active
sequence into a combined llama batch; prompt prefill is chunked for fairness. Every
slot owns its sampler, positions, output, timing, and request control. Terminal
cleanup removes only that slot's attention and recurrent state. The existing
synchronous per-request backend interface must be replaced or adapted for this
engine; adding workers around the shared context is not safe.

Phase 6B connects this engine to one llama.cpp context and the existing RPC/public path
behind explicit `--batching continuous` / `--backend-batching continuous` flags. The
configuration is capped at two active sequences and 4096 total context tokens, with a
2048-token prompt-plus-output limit per request. A short cached-model check verified
interleaving, serial-output equivalence, cancellation isolation and clean reuse. It is
not a throughput, long-context, thermal, sanitizer or C++ race-proof claim.

Phase 6C compares both modes through the public API. At two clients, the tested
continuous mode removed about one second of queue wait and reduced median TTFT, while
aggregate throughput fell slightly and completion latency rose slightly. See
[the measured report](docs/phase-6c.md); batching is therefore an explicit policy
tradeoff rather than a claimed universal speedup.

Requests have explicit states: created, queued, admitted, prefill, decoding, then
completed; rejected, failed, cancelled, and timed-out are terminal alternatives.
Transitions and cleanup will be tested before adding scheduling policies.

### Phase 2A contracts

`GenerationRequest` carries an ID, prompt, output limit, and optional monotonic
deadline. These are data fields; validation, ID assignment, and deadline enforcement
belong to the upcoming runtime. `RequestLifecycle` is a separate noncopyable atomic
state machine. It serializes state changes, not arbitrary request data or cleanup.

| Current state | Allowed next states |
| --- | --- |
| created | queued, rejected, cancelled, failed, timed_out |
| queued | admitted, cancelled, failed, timed_out |
| admitted | prefill, cancelled, failed, timed_out |
| prefill | decoding, completed, cancelled, failed, timed_out |
| decoding | completed, cancelled, failed, timed_out |
| Any terminal state | None |

No self-transitions or backward transitions are allowed. Prefill may complete with
no output (for example, immediate end-of-generation). Unknown enum values are
rejected. Competing terminal transitions use atomic compare/exchange; at most one
succeeds. A state change alone does not cancel backend work or deliver a response.

`BoundedQueue<T>` uses preallocated optional slots in a ring, protected by one
mutex. It accepts noexcept-movable items; intended runtime use is owned request
handles. `try_push(T&&)` returns accepted/full/closed without waiting for capacity,
and moves the item only on acceptance. It may wait briefly for the mutex; it is not
lock-free. Capacity limits item count, not the bytes referenced by each item.

`wait_pop()` sleeps on a condition variable until an item exists or the queue is
closed. It returns empty only after close and full drain. `close()` is idempotent,
rejects subsequent pushes, and wakes all waiting consumers. Concurrent producers
are ordered by admission under the mutex; there is no producer/consumer fairness
guarantee. `size()` is a synchronized snapshot, not a check-then-act admission API.

The owner must close and join all users before destroying the queue. Remaining
items are released on destruction. Close is graceful drain, not cancellation;
selective removal, per-request waiting deadlines, scheduling workers, byte budgets,
and automatic lifecycle changes are outside Phase 2A.

### Phase 2B orchestration

`Runtime` owns the backend, bounded queue, and one `std::thread`. `submit` returns
an admission status, a read-only lifecycle handle, and a future result. Accepted
work is executed sequentially; invalid/full/closed submissions immediately resolve
to rejected results. Caller-supplied IDs must be nonzero; uniqueness is the caller's
responsibility in this milestone. A zero output limit is rejected; empty prompts
are allowed. Phase 2E adds cooperative deadlines and cancellation, described below.

The admission mutex makes queue publication plus the created-to-queued transition
atomic relative to worker admission. The worker releases the queue lock before
taking that mutex, and no backend call runs under either lock. Capacity is waiting
items only; at most one additional item is owned by the worker.

The worker marks admitted then prefill. The synchronous backend signals decode
start exactly once before generating output; zero-output completion may omit it.
Repeated notifications or nonempty output without a notification fail the request.
Standard and unknown backend exceptions become failed results, and the worker
continues. Results and lifecycle handles may outlive the runtime. Future readiness
is the synchronization point for consuming results; observing a terminal state
alone does not mean the promise has already been fulfilled.

`shutdown` closes admission, drains accepted work, and joins. A separate mutex
serializes concurrent shutdown calls without holding admission while waiting.
Destruction invokes shutdown before destroying resources. Backend calls must finish
for shutdown to return; forceful interruption is unavailable. Backends must not
retain request/callback references, call shutdown, or destroy the calling runtime
from its worker. Callers must preserve runtime lifetime during API calls.

At Phase 2B this interface had no model load/unload, token streaming, cancellation,
metrics, or deadline enforcement. Streaming/timing extensions are described below.
The fake backend exists only in tests; its output
and timings are not model results. The real adapter will need to honor output limits
and report the actual prefill/decode boundary.

### Phase 2C native adapter

The native preset imports existing llama.cpp Release libraries after checking the
source revision and clean checkout. Model, context, sampler, and extended batch
handles use RAII. Backend-session cleanup runs after context/model destruction.
This milestone supports one native backend per process and serial generation.

The adapter accepts a local model file and verifies Nemotron-H architecture plus
the documented 4B parameter count. It is not a generic multi-model backend or an
artifact-checksum validator. The successful run uses the previously hash-verified
cached Q4_K_M file. No network/download API is invoked.

User text is wrapped in the fixed NVIDIA single-turn, no-tools, thinking-disabled
format. Embedded chat delimiters and inputs over 64 KiB are rejected. Tokenization
uses the loaded vocabulary. Prompt plus output budget must fit configured context
(at most 4096); output limits are 1-512 sampled non-EOG tokens. These bounds are
explicit; no automatic truncation or context resizing occurs.

Prefill uses explicit token positions and sequence 0, in batches of 128 by default.
The adapter synchronizes prefill, then notifies decode start before greedy sampling.
Decode evaluates one token at a time, stopping at EOG or the limit. Phase 2D emits
each token's bytes to the runtime for accumulation and optional streaming.
Per-request RAII cleanup synchronizes and clears attention/recurrent memory on
normal return or evaluation exceptions. Validation errors before evaluation leave
the context untouched. State is not reused as a prompt cache between requests.

CLI construction loads the model/context before starting the runtime worker; load
time is not a request lifecycle metric. Multi-backend lifetime coordination, chat
history/templates, model load/unload APIs, finish-reason/token-count metadata,
quality evaluation, and partial GPU-failure recovery tests remain future work.

### Phase 2D streaming and measurements

The backend generates through callbacks and no longer returns its own full output
string. The runtime counts one event per non-EOG token, including empty pieces,
appends text once, and synchronously calls the optional consumer outside locks.
Token views are borrowed until callback return. Slow consumers block the worker;
there is no extra unbounded output queue. Throwing consumers fail the request while
native cleanup clears model state. Previously streamed bytes cannot be retracted.

Submission, queue publication, admission, backend entry/return, decode start, first
and last token, and terminal-result timestamps use a monotonic clock. The final
result carries count, queue waiting, phase durations, runtime TTFT, mean inter-token
interval, and a derived decode-phase rate. Null means no applicable measurement.
Definitions and limits are in [observability](docs/observability.md). Model loading,
network/client delivery timing, aggregate metrics, and instrumentation-overhead
benchmarks are not included. CLI streaming and JSON metrics are independent flags.

The runtime owns its worker and joins it at shutdown. The backend owns model,
context, and sampler resources through RAII. Request data must outlive execution;
stream consumers must not retain borrowed token buffers. Slow consumers need
bounded buffering and a defined cancellation policy. Client disconnect/deadline
cancellation must propagate Go -> C++ -> backend, subject to backend safe points.

The current Apple standard library does not expose jthread or stop_token. The runtime
uses an explicitly joined std::thread and shared request controls, requiring no
toolchain change.

## Measurement plan

### Phase 5 memory accounting

The [memory observability contract](docs/memory-observability.md) defines two
overlapping views that are never summed: macOS `TASK_VM_INFO` current/lifetime-peak
process RSS and physical footprint, and pinned llama.cpp allocation rows for model,
hybrid context, and compute buffers. Allocation capacity is not physical residency,
pressure, or per-request ownership. Apple unified-memory placement is not presented
as separate host RAM plus VRAM.

The first implementation will sample existing lifecycle safe points and retain a
bounded latest-value set. It will isolate llama.cpp's WIP breakdown API inside the
native adapter and keep aggregate completion-window memory null. The structured
upstream API combines Nemotron attention and recurrent allocations; separate live
components remain unavailable. Phase 5C must measure and review the short one-sequence
baseline before native two-sequence batching is attempted.

### Phase 2E request control

Each submission owns a shared control handle with an immutable monotonic deadline,
atomic cancellation intent, and a completion mutex. `cancel()` records intent once;
worker checkpoints throw a typed stop cause. Completion seals the control under
the same mutex as cancellation, then publishes one terminal lifecycle/result.
Pending stop intent can replace success before sealing; later cancellation has no
effect. Already observed stops/errors preserve their selected outcome.

Expired valid submissions return a ready timed-out result; invalid/full/closed
requests retain rejection semantics. Queued stopped work retains its slot until
dequeue. The worker records admission/queue wait while checking it but does not call
the backend. No timer thread, forceful thread termination, or selective queue removal
is introduced. Graceful shutdown still drains and joins.

The native backend checks around batches/decode steps, and runtime callbacks check
before/after consumer work. These safe points cannot preempt Metal operations or a
blocked consumer. Deadline return can be late; native RAII synchronizes and clears
state before terminal publication. The upstream CPU-only abort callback is unused.
CLI --timeout-ms starts after model load. See [the report](docs/phase-2e.md).

Record monotonic timestamps for arrival, queue entry, admission, prefill, first
token, subsequent tokens, and completion. Keep client latency separate from backend
time; report actual token counts, errors, configuration, warmup, and variability.
Define metrics precisely before implementing the Python runner. Baseline upstream
performance before changing runtime code. Keep any upstream patches isolated.

## Initial risks and trade-offs

- Native llama.cpp APIs evolve: pin a commit and contain integration in one adapter.
- Nemotron uses recurrent and attention state; conventional all-attention KV-cache
  estimates do not describe the whole model.
- Local inference and checkpoint stop/recovery tests passed. Concurrency stress
  with real models and numerical parity comparisons remain untested.
- Internal gRPC needs pinned protobuf generators and native transport dependencies.
  Measure RPC costs separately from runtime timings; no performance gain is assumed.
  Public HTTP/SSE compatibility, streaming and cancellation correctness take priority.
- Thermal state and competing workloads can distort laptop benchmarks.
- Model provenance from the cached artifact is hash-pinned, but the original NVIDIA
  revision is not available in its Ollama manifest.

Go and Python benchmark modules are present; Studio remains planned. Native gRPC
will be opt-in; existing CLI/core builds must not fetch or require its dependencies.

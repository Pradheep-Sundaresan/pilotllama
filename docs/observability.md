# Request measurements and streaming — Phase 2D

All timestamps use `std::chrono::steady_clock` within one process. Durations below
are measured or derived from runtime events, not estimates. They are returned in
`GenerationResult.metrics`; CLI `--metrics` prints a JSON object to stderr alongside
upstream logs. No dashboard, aggregation, or network telemetry is implemented yet.

| Field | Unit / source | Boundary or formula |
| --- | --- | --- |
| `output_tokens` | Token events, counted by runtime | One event per sampled non-EOG token; includes empty text pieces and events before failure |
| `runtime_latency_ms` | ms, monotonic clock | `submit` entry to terminal-result timestamp, before metrics calculation/promise publication |
| `queue_wait_ms` | ms, monotonic clock | Queue publication to worker admission; excludes mutex wait before publication |
| `backend_execution_ms` | ms, monotonic clock | Backend call entry to its return or exception catch |
| `prefill_phase_ms` | ms, monotonic clock | Backend entry to decode-start callback; includes formatting/tokenization/validation and synchronized prefill |
| `decode_phase_ms` | ms, monotonic clock | Decode-start callback to backend return/exception; includes sampling, EOG checks, consumer work and native cleanup |
| `ttft_ms` | ms, monotonic clock | `submit` entry to first non-EOG token event, before invoking the consumer callback |
| `mean_inter_token_ms` | ms, derived | `(last token event - first token event) / (output_tokens - 1)` for at least two events |
| `decode_tokens_per_second` | tokens/s, derived | `output_tokens / (decode_phase_ms / 1000)` for positive count and duration |

These boundaries describe runtime-observed TTFT and phase timing. They are not
client-observed HTTP latency, pure GPU/kernel timings, or llama-bench rates. Model
loading happens before submission and is excluded. The first token event may have
an empty byte piece; TTFT is not necessarily time to the first visible character.
Every non-EOG token is counted regardless of its byte length; EOG is excluded.

Missing/unreached measurements are JSON `null`, never fabricated zeros. Rejected
requests have measured runtime latency and zero output events, with phase/TTFT
measurements absent. Failed requests preserve measurements for events that occurred;
these must not be treated as completed-request throughput. Zero-duration intervals
produce a null rate rather than infinity. Mean inter-token latency is unavailable
with fewer than two token events. Token limits count events, not bytes or words.

## Streaming contract

The native backend reports decode start after synchronized prompt evaluation, then
emits each sampled non-EOG token piece synchronously. The runtime validates state
and token budget, records event time, appends the piece once to the final result,
then invokes the optional consumer callback. The backend no longer builds another
full output string. One timestamp pair and a counter summarize token timings; there
is no unbounded token-event history or additional stream queue.

Callbacks execute on the worker outside runtime locks. Slow consumers block further
generation, so phase times/rates and later token intervals include this backpressure.
Borrowed pieces expire after callback return; copy only if retention is needed.
Captured objects must outlive completion. Callbacks must not wait on this runtime's
futures, call shutdown, or destroy the runtime.

Callback exceptions fail the request, clear its final accumulated text, and permit
the worker to handle subsequent requests. Previously delivered bytes cannot be
retracted; the completion result is the terminal success/failure signal. Native RAII
cleanup synchronizes and clears hybrid state even when the consumer throws.

CLI `--stream` writes/flushes pieces immediately to stdout and avoids printing the
final result a second time. `--metrics` is independent of streaming. Metrics JSON is
printed after completion; stdout is text, not a token-event protocol. Pieces are raw
bytes and may split UTF-8 characters. OS pipe termination and asynchronous network
disconnect handling are not implemented as a cancellation API.

## Verification and missing metrics

Unit tests use deterministic synthetic clock fixtures for formulas, never published
as performance results. Additional tests cover token ordering, empty pieces, callback
failure, bounded output count, and a deliberately blocked consumer without sleeps.
The opt-in native test checks actual token events and recovery after a thrown consumer
callback. Real measurements are presented only where their methodology is recorded.

The native core adds no percentile aggregation, queue-depth time series, memory
sampling or model-load timing. Phase 4A adds a separate point-in-time
[scheduler snapshot](scheduler-observability.md). Go's later bounded aggregates are described below;
they are not full-backend concurrent-throughput measurements. Upstream benchmark memory
measurements remain separately documented in [benchmark methodology](benchmarking.md).
Phase 2E adds cooperative stop controls; instrumentation-overhead benchmarking
remains future work. No performance improvement is claimed.

Phase 5A subsequently defines the separate [memory observability contract](memory-observability.md).
It is a design boundary only: current endpoints still expose no memory value, and
the aggregate `memory_bytes` field remains null until a metric with matching window
semantics exists.

## Stop-result measurements (Phase 2E)

Go gRPC per-call timing, received-token counts and stream-rate observations are
implemented in the Go adapter with the field boundaries described here.
They are separate from native timing. Phase 3H-1 exposes them through authenticated
[bounded request-history endpoints](request-observability.md), alongside native
measurements and separate public/transport/native outcomes. Phase 3H-2 adds
[windowed retained-sample aggregates](aggregate-observability.md), with documented
quantile thresholds and completion-attributed token throughput; memory and aggregate
queue depth remain unavailable.

Cancelled/timed-out results preserve observations up to stop and cleanup. They have
no fabricated successful output rate: phase values appear only for phases reached,
and any derived rate describes observed events before stopping. Do not aggregate
them as completed throughput. Queued stops have queue-wait/admission timestamps
when dequeued for checking, but no backend duration. Already expired submissions
have neither queue nor backend timings. Cancellation latency itself is not measured.
Deadlines are cooperative; runtime latency can exceed the requested budget. Partial
delivered bytes remain delivered even when the terminal result text is cleared.
Request control remains cooperative; it does not preempt in-flight Metal work.

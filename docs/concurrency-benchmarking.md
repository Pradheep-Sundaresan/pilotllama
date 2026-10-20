# Concurrency benchmark methodology

Phase 4B adds a Python 3.12 standard-library harness for controlled requests through
Inferno's public streaming API. It connects only to an explicit loopback address,
uses the existing API token from an environment variable, and never records the
token. It does not start, stop, or reconfigure the backend.

```sh
export INFERNO_AUTH_TOKEN='the same token used to start inferno-api'
.venv/bin/python benchmarks/runners/concurrency_baseline.py
```

The default workload first completes and records one serial warm-up request, excluded
from every case summary. It then keeps 1, 2, then 4 client streams active, with three
sequential requests per client and 64 requested output tokens per request. A barrier
aligns the first request at each level. The result directory contains raw `results.json` and a
one-row-per-level `summary.csv`; it stays below `benchmarks/results/` and is ignored
by version control. Run the real Metal experiment only as a separately approved
benchmark milestone.

The workload records its prompt, model alias, declared artifact SHA-256,
quantization, and declared native and Go admission/runtime settings. The runner records Apple hardware/OS,
Python version, the pinned llama.cpp revision, public model discovery, and the
initial scheduler response. The serving API does not expose the loaded file hash,
quantization, or full native launch arguments, so the manifest marks these values as
declared and records server-side artifact verification as unavailable. Native
process RSS is also unavailable through this API and is stored as null with its
reason rather than estimated. A fair
comparison requires the same artifact, prompt, output limit, runtime settings,
concurrency levels, repetitions, and scheduler sampling interval.

## Measurement definitions

| Metric | Source / unit | Method |
| --- | --- | --- |
| Client TTFT | Python monotonic clock, ms | POST start to the first non-empty SSE content string; role-only and empty events do not count |
| Client latency | Python monotonic clock, ms | POST start through response-body consumption or rejection; includes every completed, incomplete, and failed attempt |
| Response headers | Python monotonic clock, ms | POST start until HTTP response headers become available; Inferno currently commits streaming headers with its first deliverable event |
| Client content events/bytes | Parsed SSE, events/UTF-8 bytes | Non-empty content deltas delivered to the client; these are not model-token counts |
| Server request metrics | Exact authenticated request-history record | Correlated by `X-Request-ID`; absent IDs or evicted/unavailable records remain null |
| Completion-attributed throughput | Correlated native output tokens / case wall duration, tokens/s | Includes client coordination, request sequencing, queueing, and idle gaps; excludes failed or uncorrelated requests |
| Scheduler occupancy | Repeated C++ status snapshots, requests | Independent polling; max waiting/executing and busy fraction describe samples, not a continuous trace |
| P50/P95/P99 | Nearest-rank percentile, ms | P50 is shown for one or more samples; P95 requires 20 and P99 requires 100, otherwise null |

Scheduler polling competes lightly with the workload for localhost and gRPC status
work. Its 50 ms interval can miss short queue transitions. The sample timestamp is
captured after each response and the server's own observation time is retained.
Client timings include local HTTP, Go handling, gRPC, native execution, and stream
delivery. Native timings retain their narrower documented boundaries. Python socket
timeouts bound stalled operations; the Go request deadline remains the authoritative
end-to-end cancellation budget.

The harness preserves rejected and incomplete requests. It never converts bytes or
SSE chunks into token estimates, never substitutes client timing for native timing,
and never interprets sampled occupancy as an average queue depth.

## Queue versus continuous mode

`batching_comparison.py` starts two sequential managed processes with the same cached
model and `batching-comparison.json` workload. It changes only the explicit scheduler
mode, disables the Go request-rate policy, records authenticated memory snapshots,
hashes delivered content without storing it in the comparison CSV, and cleanly stops
each child. It records macOS memory pressure and reports thermal context unavailable
unless structured values are returned.

```sh
.venv/bin/python benchmarks/runners/batching_comparison.py /absolute/path/to/existing-model.gguf
```

The runner is localhost-only, generates an ephemeral bearer token, stores no token,
downloads nothing, and refuses output outside the project. Its small default workload
is a bounded comparison rather than a stress test.

# Bounded aggregates — Phase 3H-2

Authenticated `GET /internal/v1/metrics` in gRPC mode summarizes the existing
[request-history ring](request-observability.md). Authentication must be configured
(otherwise 403); missing/wrong credentials return 401 when configured. It is read-only,
does not charge generation rate credit, and adds no backend calls, workers or storage.
Legacy HTTP probe mode has no metrics endpoint.

## Scope and window

The cohort is **retained Generate attempts finalized in the last 60 seconds**, or
since Go Service construction if younger. Each record has a private monotonic Go
completion timestamp; clock-based selection is inclusive at the start/end. This
timestamp is not inferred from the exposed wall-clock completion time. The endpoint
snapshots at most 128 records, then computes outside the ring lock. Completions after
that snapshot appear on a later poll; no exact all-request snapshot is promised.

| Field | Source/unit/method |
| --- | --- |
| `window_start_unix_ms`, `window_end_unix_ms` | Selected observation bounds expressed as Go wall-clock Unix milliseconds for display |
| `window_duration_ms` | Monotonic elapsed milliseconds between the bounds; at most 60000, shortened during startup |
| `history_capacity` | Fixed ring capacity, 128 records |
| `retained_records` | Ring entries scanned, including entries outside the time window |
| `retention_at_capacity` | True when all 128 ring slots are populated; signals possible eviction/incomplete time-window coverage, not proof of a specific number dropped |
| `samples` | Retained records with completion timestamp inside the window |
| `completed`, `failed`, `delivery_failed` | Counts by the request-history public outcome; these partition `samples` |
| `completed_native_output_tokens` | Sum of native output-token counts from `completed` records only; excludes failed and delivery-failed records even if they generated partial tokens |
| `completed_native_tokens_per_second` | Above token sum divided by actual observation-window seconds, including idle time; null for zero duration, zero if positive duration has no qualifying tokens |
| `queue_depth`, `memory_bytes` | Always null: neither is sampled over this aggregate window. Phase 4A's separate scheduler endpoint exposes current occupancy only. |

Token throughput is **completion-attributed**, using native tokens on requests that
finished in the window; those tokens may have been generated before the start bound.
It is neither instantaneous token emission rate, an average of decode rates, nor
complete backend throughput when retention saturates. Pre-RPC rejections, in-flight
requests, external backend clients and evicted entries are not represented. No
lifetime totals, rejection rate, success percentage or native idle inference is made.

## Distributions

`handler_latency_ms`, `rpc_latency_ms`, `rpc_ttft_ms`, `native_runtime_latency_ms`,
`native_ttft_ms`, and `native_decode_tokens_per_second` summarize only `completed`
records in that cohort. Each has its own `samples`, `p50`, `p95`, `p99`; missing
optional source values are excluded, not replaced by zero. Nonfinite/negative values
are defensively excluded. Source units are milliseconds, except native decode rate
(tokens/second). Boundaries/formulas remain exactly those in
[request observability](request-observability.md) and [native observability](observability.md).
In particular RPC TTFT and native TTFT are separate distributions.

For each field, sort its valid samples ascending and take the nearest rank
`ceil(p * N)`, indexed from one. P50 requires at least one sample; P95 requires at
least 20; P99 requires at least 100. Below those thresholds the corresponding value
is null. These are empirical sample quantiles, not confidence bounds or proof of
stable population tails. The 128-record limit keeps tail resolution coarse. Empty
distributions have zero samples and all quantiles null.

This endpoint does not change compatible JSON/SSE completion envelopes. A
`completed` record is validated completion, not proof of remote client delivery;
JSON delivery is unobserved, as described in the underlying history contract.

## Verification

Model-free tests verify exact nearest-rank values, independent sample thresholds,
missing/nonfinite values, time-window exclusion, short startup duration, failure
cohort exclusion, zero-duration rates, bounded-ring saturation, eviction and auth.
The existing live HTTP/gRPC fixture test verifies two completed requests, eight
native tokens, positive measured duration, a derived rate, and suppressed P95 for
two latency samples. Those are model-free correctness checks, not model benchmarks.
Go race tests, vet/build and the fifteen live HTTP/gRPC regression cases passed.
No new dependencies, native inference changes, model downloads, native rerun or
performance improvement is claimed.

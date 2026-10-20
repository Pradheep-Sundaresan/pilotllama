# Scheduler snapshot — Phase 4A

Authenticated `GET /internal/v1/scheduler` reports a point-in-time snapshot from the
C++ runtime. Go obtains it through the existing localhost `GetStatus` gRPC and adds
the observation time and RPC latency. Authentication must be configured; missing or
wrong credentials return 401, and disabled control authentication returns 403. The
endpoint performs no generation, admission mutation, background sampling, or storage.

Example when the single worker is executing and both waiting slots are occupied:

```json
{
  "observed_at_unix_ms": 1792240000000,
  "source": "cpp_runtime_status_snapshot",
  "unit": "requests",
  "backend_state": "ready",
  "waiting_requests": 2,
  "executing_requests": 1,
  "queue_capacity": 2,
  "status_rpc_latency_ms": 0.31
}
```

## Source and boundaries

| Field | Source, unit and method |
| --- | --- |
| `waiting_requests` | Request items. Incremented after the bounded C++ queue accepts an item and the runtime publishes `queued`. Decremented when the worker publishes `admitted`. A dequeued item remains counted as waiting until that transition, so it does not disappear between states. |
| `executing_requests` | Request items. Set to one in the same admission-lock section that publishes worker admission, and cleared after terminal state/metrics are sealed but before completion is published. The current runtime has exactly one worker, so the value is zero or one. |
| `queue_capacity` | Request items. Immutable capacity of the C++ bounded waiting queue, excluding the executing request. It does not bound prompt/output bytes, RPC-active entries, or Go handlers. |
| `backend_state` | gRPC status enum from the same response, rendered as a stable lower-case name. Readiness is still a separate concept from queue occupancy. |
| `status_rpc_latency_ms` | Milliseconds, Go monotonic clock. `GetStatus` call entry through validated response return. It measures local control-plane observation latency, not inference latency. |
| `observed_at_unix_ms` | Go wall-clock Unix milliseconds immediately after the status response is received, for display correlation. It is not the exact C++ lock-acquisition instant. |

The three C++ occupancy values are copied while holding the runtime admission mutex.
They are a coherent snapshot of the runtime counters. They do not describe requests
that have not crossed C++ admission, including Go handler work, public-capacity or
rate-limit rejections, and the RPC adapter's separate active-request bound.

This is not continuous sampling. It has no historical maximum, average queue depth,
arrival rate, or occupancy duration, and a short wait can occur entirely between
polls. Consequently the 60-second aggregate report's `queue_depth` remains null.
Later concurrency experiments can add an explicitly sampled time series without
relabeling these snapshots as an aggregate.

## Verification

The C++ gated-runtime test verifies idle, one executing, two waiting/full, and drained
snapshots around real worker transitions. Go tests cover response validation, auth,
missing measurement handling, and exact HTTP fields. The model-free live integration
suite verifies the C++ status message through gRPC and the authenticated Go endpoint;
all fifteen cases pass. The full 43-test C++ executable, full Go race suite, vet,
binding consistency, and Studio production build pass. The native server and Go API
binaries were rebuilt from the existing project-local dependencies. No Metal model
run, dependency download, or performance claim is part of this milestone.

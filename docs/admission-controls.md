# Admission drain/resume — Phase 3G-1

These Go-only controls pause new generation requests while letting already
admitted handlers finish normally, subject to their existing deadlines and errors.
They do not stop, unload, restart or cancel the native model/backend.
Separate [managed backend controls](backend-controls.md) can stop/start the owned
child after closing admission and checking active Go handlers; native idle remains
unknown until process exit is actually joined.

In gRPC mode with `--auth-token-env VARIABLE` configured:

| Endpoint | Behavior |
| --- | --- |
| `GET /internal/v1/admission` | Observe local admission state |
| `POST /internal/v1/admission/drain` | Close generation admission immediately |
| `POST /internal/v1/admission/resume` | Reopen generation admission immediately |

Supply the configured `Authorization: Bearer TOKEN` header. Without configured
authentication these endpoints return 403 `control_auth_required`, even though
generation can remain unauthenticated. Missing/wrong credentials return 401 when
auth is configured. No extra administrator role is implied by the shared token.
Legacy HTTP probe mode exposes none of these endpoints. Mutations take no body or
query parameters and return 400 if supplied. Drain and resume are idempotent,
nonblocking operations; poll status rather than holding a request open to drain.

Example response:

```json
{"state":"draining","active_generation_handlers":1,"native_idle":null}
```

## Meaning and measurement

- `state` is `accepting` while the gate is open, `draining` while closed with active
  generation handlers, or `drained` while closed with zero generation handlers.
  It is local admission state, not model load, process health or C++ readiness.
- `active_generation_handlers` is a measured integer count (unit: handlers).
  Source: a Go mutex-protected counter incremented immediately after the authenticated
  request crosses the gate, before rate checks, body validation and existing capacity
  admission; decremented when that downstream handler returns, including error paths.
  It excludes authentication/drain rejections, model discovery and health/control
  requests. A snapshot includes brief rate/capacity/validation checks as well as
  inference handlers; it is not queue depth, executing native requests or throughput.
- `native_idle` is always null: native idle/cleanup completion is not observed by
  this API. An RPC deadline/disconnect may finish a Go handler while C++ cleanup is
  still running. **`drained` does not authorize assuming the native backend is idle.**

Gate changes and counter entry are serialized by the same mutex. Once drain returns,
any request that crosses the gate afterwards is rejected; requests already counted
may continue through validation and inference. Concurrent resume may reopen admission
immediately. Responses are point-in-time snapshots, not leases or a native drain barrier.
State starts accepting on each new Go service and is not persisted.

## Request behavior

The order is authentication → drain gate → generation rate policy → existing
capacity/body/model checks → inference. Drain rejections return 503
`admission_draining` JSON before application body parsing, rate-credit consumption,
request-ID allocation, or SSE. Existing admitted requests retain their rate/capacity
rules and are not cancelled by drain. Drain has no background thread or waiting queue.

`/readyz` returns 503 with reason `admission_draining` while the gate is closed.
`/healthz` remains independent, and authenticated model discovery still works.
Controls do not consume generation rate credits or public-operation slots, so they
remain available during saturation. Resume opens the Go gate; actual generation and
readiness still require the C++ backend to be ready.

## Verification and limits

Model-free race tests cover an active request completing through drain, 32 concurrent
post-drain requests rejected before backend work, idempotent controls, counted handler
completion, readiness/health/discovery behavior, successful generation after resume,
authentication, invalid control queries, and rate-ordering. A live C++ fixture verifies
authenticated HTTP drain, rejection, resume and generation. Existing HTTP/gRPC tests
remain passing. No native/GPU rerun, native idle instrumentation, active native drain,
model lifecycle mutation, or performance improvement is claimed here.

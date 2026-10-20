# Managed backend stop/start — Phase 3G-2

The Go CLI now uses a restartable manager in `--backend-exec` mode. Its HTTP service
stays running after an explicit backend stop or unexpected child exit. It can start
the same configured executable/model again, on a fresh OS-assigned localhost port,
without changing the public HTTP address. There is no automatic restart or model
selection/download API. Initial CLI startup still waits for live model readiness
before starting HTTP.

The configured scheduler mode is stable across restarts. Queue mode remains the
default; `--backend-batching continuous` opts into the Phase 6B two-sequence engine.

## Authenticated controls

Configure `--auth-token-env VARIABLE` and send its bearer token. Without configured
authentication controls return 403; missing/wrong credentials return 401 when auth
is configured. All endpoints remain loopback-only.

| Endpoint | Meaning |
| --- | --- |
| `GET /internal/v1/backend` | Process-manager state and Go admission snapshot |
| `POST /internal/v1/backend/stop` | Pause admission and stop/reap the owned child |
| `POST /internal/v1/backend/start` | Pause admission and await a ready replacement child |

POST operations accept no body or query parameters. Status is available during an
operation. Lifecycle mutations bypass generation rate/capacity admission but are
serialized without a waiting queue: overlapping mutations or admission drain/resume
return 409 `lifecycle_busy`. External backends return status `external`, `managed:false`;
attempting to start/stop one returns 409 `backend_not_managed`. No external process
is signalled.

Both start and stop first close the Go generation gate. If any Go generation handler
is active, they return 409 `generation_active` **without touching the child**. The gate
remains closed: poll admission until handlers finish and retry. As documented in
[admission controls](admission-controls.md), zero handlers does not prove native idle.
Stop uses native SIGTERM shutdown and waits for process exit/reap, rather than
assuming RPC completion equals native cleanup. The configured shutdown grace still
applies; exceeding it kills/reaps the owned child and returns a failure, never a
successful graceful-stop claim.

Start waits for the existing live gRPC readiness and matching-alias checks. It uses
only the original executable, local weights, alias and startup budget. Successful
start **preserves admission drain**. Explicitly POST `/internal/v1/admission/resume`
to accept generation again. Repeated start while already ready or stop after a clean
stop does not launch another child. These calls still close the gate and check for
active handlers, so they are not side-effect-free health probes.

The manager reports `stopped`, `starting`, `ready`, `stopping`, or `failed`. This is
process-cycle state, not continuous inference health; readiness still probes C++.
Unexpected child exit or failed startup/cleanup records `failed` while HTTP stays
alive. Failed startup is fully joined/cleaned before another start is allowed. A
failed stop reports 503 `backend_operation_failed`; retrying stop can continue to
report the recorded failure even after reaping. An explicit start can recover from
an ended failed cycle. There is no retained raw error text, PID-control API or
arbitrary process/model/path input in HTTP responses.

While stopped, health remains 200; readiness and generation are unavailable. Model
discovery returns backend unavailable. The manager's stable RPC facade replaces its
underlying gRPC connection on each start. A concurrent discovery call can fail when
the old connection closes; there is no cross-cycle retry or readiness lease.

Caller cancellation during startup cancels and joins the startup cycle. Once stop
begins it joins cleanup even if the HTTP client disconnects. Application shutdown
still cancels/joins HTTP serving, then closes the manager and joins its child cycle.
Cleanup can outlast HTTP shutdown's five-second grace; native shutdown has its own
bounded grace/forced-kill fallback. Forced kill cannot guarantee application cleanup.

## Verification

Model-free Go race tests cover failed-start recovery, repeated stop/start, idempotent
child operations, overlapping-operation rejection, cancelled startup, unexpected exit,
forced kill reporting, closed-manager rejection, active-handler stop refusal,
authentication and status/health availability during a blocked start.

`sh scripts/test-managed-backend.sh` uses the existing real C++ model-free fixture:
generation → HTTP stop → health alive/readiness unavailable → HTTP start → explicit
resume → streaming generation → stop, with owned-child reaping and state cleanup.
It also retains the earlier two-cycle fixture test. Existing fifteen HTTP/gRPC
integration cases remain passing; Go race tests, vet and the production build passed.

Phase 3G-2 itself used model-free verification. Phase 3G-3 (2026-10-17) extended
and passed `sh scripts/test-native-managed.sh EXISTING_LOCAL_GGUF` with the cached
Nemotron 4B Q4_K_M model and existing native Metal executable. One Go HTTP test server
stayed running through this sequence:

1. Initial managed load/readiness, then authenticated JSON and SSE arithmetic
   requests, both returning `4` with a one-token length finish.
2. Authenticated stop, successful child exit/reap, health 200, readiness/discovery
   503, and generation rejected while stopped.
3. Authenticated start of the same model, live readiness verified by the manager,
   public readiness still 503 until explicit admission resume, then readiness 200.
4. JSON and SSE again returned `4`; a final authenticated stop cleanly exited/reaped
   the second child. The state directory was empty.

The native test passed with Go's race detector; no production-code fixes were needed.
The test uses production Manager/Service code and real localhost HTTP/gRPC sockets,
with `httptest.NewServer` supplying HTTP serving. It does not exercise the CLI parser
or `Service.Run` signal handling. Requests completed before each stop: native active
drain, forced-kill recovery on Metal, alternate models and arbitrary model swaps
remain unverified. This is a correctness check, not a benchmark, memory measurement,
C++ sanitizer pass or data-race proof. No dependencies, models, system settings,
drivers or hardware configuration were changed.

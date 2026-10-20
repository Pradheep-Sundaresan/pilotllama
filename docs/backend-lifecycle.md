# Managed local backend — Phase 3D-1

Current CLI behavior is extended by [Phase 3G-2 stop/start controls](backend-controls.md).
It now keeps HTTP alive across managed child stop/failure and supports explicit restart.
Phase 3G-3 verified that manager's authenticated native stop/start/resume sequence;
see the same control document for the exact evidence and limits.

Go can optionally own one C++ inference process. External `--backend-grpc` and
legacy `--backend` modes remain available. This milestone adds process lifecycle
coordination, not a model catalog or hot load/unload API.

From the project root, with the existing native server built and existing local
weights (no downloads):

```sh
sh scripts/go-local.sh build -o ../build/inferno-api ./cmd/inferno-api
./build/inferno-api \
  --backend-exec "$PWD/build/grpc/inferno_rpc_server" \
  --model /absolute/path/to/existing-model.gguf \
  --model-id local-nemotron-4b
```

Managed scheduling defaults to `queue`. Phase 6B adds the explicit
`--backend-batching continuous` option for the bounded two-active-sequence native
engine. The manager preserves it across stop/start cycles. See
the tested scope and limits described in this document.

Executable and model must be absolute existing regular files; the executable must
have execute permission. Use only a trusted native executable. Go invokes it
directly, without a shell. C++ still validates/loads the supported model and owns
inference. Managed mode rejects explicit external HTTP/gRPC backend settings.
The model alias follows the native server's ASCII format; it is not a verified hash.

## Startup and monitoring

- A private temporary directory under `build/backend` (relative to the current
  directory) holds the child's new ready file. `--backend-state-dir` overrides it;
  keep this in the project or an explicitly approved user-owned location.
- The child binds `127.0.0.1:0`, allowing the OS to choose an available port. Go
  reads the port and checks live gRPC `GetStatus`: READY and the configured alias
  must both match. A file alone does not establish readiness.
- Readiness is polled at 50 ms intervals with existing bounded status calls and a
  default two-minute startup budget (`--backend-startup-timeout`, maximum one hour).
  HTTP starts only after readiness. During model loading HTTP health is therefore
  unavailable; once running, existing health/readiness and generation checks apply.
- One joined wait goroutine monitors child exit. The original Phase 3D-1 `Run`
  wrapper cancels/joins its serving callback on unexpected exit. The Phase 3G-2 CLI
  manager retains HTTP, reports failed state and permits an explicit new start.
  Neither mode automatically restarts, retries inference or loads a second model.
- Child logs inherit the user's output streams; Go retains no log buffer. There
  are no new exposed metrics, performance claims, or dependencies.

## Shutdown and limits

On application shutdown the child has its own process group so terminal signals do not bypass Go's shutdown
order. On cancellation, Go cancels HTTP/RPC requests and waits for HTTP shutdown,
then sends SIGTERM to its owned child and waits for exit. Startup failure and HTTP
startup/serve failure also clean up the child. Existing external backends are never
signalled. Temporary state is removed after the child is reaped.

Native stop remains cooperative. If the child does not exit within the default
15-second grace (`--backend-shutdown-timeout`, maximum one minute), Go kills and
reaps that child and reports an error. Forced termination does **not** mean native
cleanup or inference completed; this is not a graceful request-drain guarantee.
No process-tree supervision, arbitrary model swap, download, system
setting, driver or hardware change is introduced. Use the C++ server executable
directly, rather than a wrapper that launches unrelated descendants.

## Verification

Model-free Go race tests cover configuration rejection, startup exit, readiness
timeout/non-ready state, cancellation while loading, normal shutdown, unexpected
exit, HTTP callback failure, forced stop and temporary-state cleanup. The existing
real C++ RPC fixture also passed two start/readiness/generate/stop cycles using live
gRPC and the shared model alias. That fixture adapter translates the native command
arguments to its test-only CLI; Phase 3D-1 did not exercise native model loading.

Phase 3D-2 (2026-10-17) verified the production `backendprocess.Run` path with the
existing native executable and cached Nemotron 4B Q4_K_M weights on Metal. One
managed process passed live HTTP health/readiness and two sequential arithmetic
requests: JSON and SSE both returned `4`, with a one-token `length` finish; the SSE
stream preserved its completion ID and ended with `[DONE]`. The callback's real
HTTP test server then closed, the native child exited successfully without a forced
kill, Go reaped it, and the temporary state directory was empty. No production-code
fixes were needed.

This opt-in check uses the production lifecycle manager and public HTTP handler,
with `httptest.NewServer` providing real HTTP sockets; it does not exercise the CLI
flag parser or `Service.Run` signal handling. Earlier model-free checks cover those
HTTP shutdown mechanics separately. Native shutdown here occurs after requests
complete: no new active-request drain, startup-cancellation, performance, memory,
or universal model compatibility claim is made.

```sh
sh scripts/go-local.sh test -race ./...
INFERNO_RPC_FIXTURE="$PWD/build/grpc/inferno_rpc_fixture" \
  sh scripts/go-local.sh test -race -count=1 ./internal/backendprocess -run '^TestIntegration' -v
sh scripts/test-native-managed.sh /absolute/path/to/existing-model.gguf
```

Localhost tests can require sandbox approval. No native/GPU performance or sanitizer
claim follows from model-free process and transport checks.

# Go control plane — Phase 3A

Phase 3D-1 optionally lets Go own the local C++ backend: see
[managed backend lifecycle](backend-lifecycle.md) for flags, shutdown and limits.

Implemented: a Go service using the standard library for local HTTP health/readiness,
validated configuration, bounded backend probes, and signal-driven shutdown.
Requires Go 1.25+. The module also contains generated gRPC bindings; see
their separately provisioned dependencies and checks.
No dependencies or models are downloaded by these commands.
Phase 3C enables [public chat completions](public-api.md) in --backend-grpc mode;
legacy HTTP probe mode still has no generation route.
Phase 3B-3 adds opt-in gRPC readiness: use `--backend-grpc 127.0.0.1:8081`
instead of `--backend`.
The HTTP probe/default remain compatible; explicit use of both modes is rejected.
Run from the project root; all generated Go files stay under `build/`:

```sh
export GOCACHE="$PWD/build/go-cache"
export GOPATH="$PWD/build/go-path"
export GOMODCACHE="$PWD/build/go-mod"
export GOPROXY=off GOTOOLCHAIN=local
cd controlplane
go test -race ./...
go vet ./...
go build -o ../build/inferno-api ./cmd/inferno-api
../build/inferno-api --listen 127.0.0.1:8080 --backend http://127.0.0.1:8081
```

The exports affect the current terminal only; they do not edit shell configuration.
Stop with Ctrl-C or SIGTERM. Shutdown cancels in-flight probes and waits up to five
seconds for HTTP handlers, closing connections if the wait expires. The serving
goroutine is joined. These shutdown settings must be revisited before adding streams.
The startup log announces an attempt to bind; a bind failure exits with an error.

| Endpoint | Implemented behavior |
| --- | --- |
| `GET /healthz` | 200 JSON: service is handling HTTP; independent of backend |
| `GET /readyz` | 200 `{"ready":true}` only after compatible backend readiness; otherwise 503 `{"ready":false,"reason":"backend_unavailable"}` |
| `POST /v1/chat/completions` | Documented JSON/SSE subset in gRPC mode; 404 in legacy HTTP mode |
| Other paths | 404 |

GET routes also support HEAD. Unsupported methods on known routes return 405.
Health responses use `Cache-Control: no-store`. Probe failures expose no internal
errors. Readiness is checked on demand, not cached; it is not an admission guarantee.
Requests use explicit loopback IPs (IPv4 or IPv6), ports 1–65535, and HTTP.
Wildcard addresses, DNS names, credentials, URL paths, queries and fragments are
rejected. Backend redirects and environment proxies are disabled. Probes have a
one-second total timeout, a 4096-byte response limit and at most eight connections
to the backend. HTTP headers are limited to 16 KiB plus Go's parser allowance;
header/body-read/idle timeouts are 2/5/30 seconds; JSON/SSE writes use rolling
five-second deadlines and generation has a separate request budget. There is no global client-count
limit or rate limiter yet. Loopback restricts network exposure; it does not provide
authentication against other local processes. Authentication remains future work.

## Architecture update — Phase 3B-1

The planned internal HTTP/SSE generation and cancellation interface is superseded
by [gRPC/Protocol Buffers](grpc-contract.md). Phase 3C implements a documented public Go HTTP/REST and SSE subset;
see [supported fields and limits](public-api.md). The new source contract
is [inference.proto](../proto/inferno/v1/inference.proto). See the
[dependency and generation plan](grpc-dependencies.md) before provisioning tools.

Phase 3A's HTTP readiness probe remains intact for compatibility. Phase 3B-3 adds
an explicit gRPC mode using GetStatus: only READY produces public 200 readiness.
The project provides a standalone native RPC server, started explicitly
with local weights. No native model server is launched automatically. Both modes preserve public
health responses and graceful HTTP shutdown; the public inference subset forwards only through gRPC.

### Transitional readiness (implemented probe, backend not implemented)

`GET /internal/v1/ready` returns status 200 with
`{"api_version":"1","ready":true}` only when a compatible backend is ready.
Go accepts additional fields but requires version `"1"` and boolean `ready:true`.
Malformed, oversized, incompatible and non-200 responses are unavailable.
No model is loaded/downloaded by a readiness request. The native C++ runtime has
no HTTP server, so readiness remains 503 in the normal CLI-only configuration.
The Phase 3A transport smoke used a controlled backend, not a native inference server.

## Verification

Go tests cover local address validation, health independence, readiness status and
schema failures, response-size limits, probe cancellation, proxy/redirect policy,
method handling, and rejection of unsafe/cancelled startup. They use in-memory HTTP
recorders and controlled transports: they do not simulate inference performance.
A separate real localhost process smoke check verifies liveness, a controlled
backend reporting not-ready/ready, an unreachable backend, and SIGTERM shutdown.
The controlled backend tests transport behavior only; it does not run inference.
Verified 2026-10-17 with Go 1.25.5 darwin/arm64: five top-level Go tests (including
26 configuration/readiness subtests) passed with `-race`; `go vet`, binary build,
and the real localhost smoke check passed. The socket test required sandbox
escalation. Logs/script are under ignored `build/phase-3a-*`. No C++ files changed,
so previous C++/native results were not rerun or relabeled as new verification.

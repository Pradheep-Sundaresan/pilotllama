# gRPC dependency and binding plan

Phase 3B-2 provisions the separately approved local tooling and generates bindings.
Build and regeneration instructions are recorded in `scripts/`.
The original dependency selection and approval estimates are retained below.
Go 1.25.5 and the existing CMake/Ninja/Apple compiler are used; no global tools changed.

## Recorded pins and official sources

| Component | Pin / source | Purpose |
| --- | --- | --- |
| C++ gRPC and `grpc_cpp_plugin` | [grpc/grpc v1.76.0](https://github.com/grpc/grpc/releases/tag/v1.76.0) | Native transport and C++ service generator |
| C++ protobuf runtime and `protoc` | `74211c0dfc2777318ab53c2cd2c317a2ef9012de` (C++ 6.31.1 / protoc 31.1) | Build generator and runtime from the same source revision; do not mix an unrelated protoc binary/runtime |
| Go gRPC runtime | [grpc/grpc-go v1.75.1](https://github.com/grpc/grpc-go/releases/tag/v1.75.1) | Go channel, stream and RPC status handling |
| Go protobuf runtime and `protoc-gen-go` | [protobuf-go v1.36.9](https://github.com/protocolbuffers/protobuf-go/releases/tag/v1.36.9) | Go protobuf encoding and message generator |
| `protoc-gen-go-grpc` | [v1.5.1](https://github.com/grpc/grpc-go/releases/tag/cmd%2Fprotoc-gen-go-grpc%2Fv1.5.1) | Go service generator |

These are explicit release selections, not a claim of newest versions. Local
compilation and protobuf wire interoperability are now verified; live gRPC network
behavior remains unimplemented. gRPC-Go's pinned [go.mod](https://github.com/grpc/grpc-go/blob/v1.75.1/go.mod)
requires Go 1.23.0; our installed Go is newer. Binding compilation passed locally in Phase 3B-2.
C++ uses gRPC's [module protobuf provider](https://github.com/grpc/grpc/blob/v1.76.0/cmake/protobuf.cmake),
which can build protobuf and protoc together from its pinned subtree.
Resolved parent/submodule commits are recorded in
[grpc-revisions.json](../third_party/grpc-revisions.json). Sources use Git clones,
not source archives. Go module checksums are recorded in controlplane/go.sum.
Do not replace pins silently if the toolchain fails: report the issue and scope.

## Approval boundary and local destinations

A subsequent dependency proposal must enumerate the actual transitive sources and
sizes before fetching. gRPC C++ has dependencies such as Abseil, protobuf, RE2,
address resolution, compression and TLS code. Inspect the pinned
[.gitmodules](https://github.com/grpc/grpc/blob/v1.76.0/.gitmodules) and CMake configuration;
fetch only required submodules, at their gitlink commits. Do not recursively fetch
benchmark/test/telemetry/Envoy sources by default. Source dependencies with those
names do not imply permission to deploy infrastructure. Avoid external test suites,
unused language plugins, automatic FetchContent downloads and telemetry exporters.
Keep future TLS support possible without enabling remote access now.

Implemented layout:

- `third_party/grpc/`: ignored, pinned C++ sources and required submodules.
- `build/grpc/`: local compiler/runtime build; no global install.
- `.tools/grpc/bin/`: local Go generator binaries.
- `build/go-mod/`, `build/go-path/`, `build/go-cache/`: Go sources and caches.
- `controlplane/gen/inferno/v1/`: committed generated Go bindings.
- `build/grpc/generated/inferno/v1/`: generated C++ bindings, recreated by opt-in build.

Planning estimates, **not measured download sizes**: approximately 100–300 MB of
compressed C++ source/dependencies and 10–50 MB of Go modules/tools; allow roughly
2–6 GB for extracted sources, caches and C++ build output. Exact totals depend on
selected transitive dependencies and build flags. This is larger than Phase 3A's
standard-library service; obtain separate approval for a concrete fetch/build plan.
No models are needed. No Homebrew changes, sudo, global PATH edits or system installs.
No Go toolchain download: keep `GOTOOLCHAIN=local`; use an approved proxy/network
window only while fetching approved module versions, then restore offline builds.

## Reproducible generation plan

Input: `proto/inferno/v1/inference.proto`; include root: `proto/`.
[generate-proto.sh](../scripts/generate-proto.sh) invokes local protoc with explicit
plugin paths, checks tool versions and compares/copies only expected outputs.
`--check` verifies all six generated files without overwriting existing outputs.
CMake automatically generates the four C++ files inside the opt-in build.
Native gRPC remains optional so ordinary
core builds never fetch or require it. Health uses the minimal shared GetStatus
contract; no extra health proto, grpc-gateway, Buf, Python gRPC or validation plugin.

## Verification boundary

Phase 3B-1 reviewed schema/semantics without a compiler. Phase 3B-2 adds real
protoc generation, native binding compilation, serialization tests in both languages,
and two-way wire interoperability checks. This does not implement or verify live
inference RPCs, network metrics or OpenAI-compatible generation. See the
the verification commands and limitations in this document.

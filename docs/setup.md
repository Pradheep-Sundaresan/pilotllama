# Development setup

## Automated readiness and build

On a fresh clone with the listed system prerequisites installed:

```sh
./scripts/doctor.sh
./scripts/setup.sh --check
./scripts/setup.sh
```

The doctor and `setup.sh --check` are read-only. Normal setup first checks whether
dependencies are already complete. If anything is missing, it lists the official
sources, local destinations and planning size, then requires explicit confirmation
before downloading exact recorded pins. It never downloads models, uses sudo, or
installs system tools. Automation can provide the same explicit authorization with:

```sh
PILOTLLAMA_ACCEPT_DEPENDENCIES=1 ./scripts/setup.sh
```

The scripts provide actionable build diagnostics and dependency provisioning.

## Start and stop the local service

After setup, select an existing local GGUF on the first start:

```sh
./scripts/start.sh \
  --model /absolute/path/model.gguf \
  --model-id local-model \
  --batching queue
```

The script starts the Go API on `127.0.0.1:8080`, which owns the native C++ backend,
serves Studio at `http://127.0.0.1:8080/`, and waits for live readiness. It creates a private bearer token and stores the token,
model selection, PID and log under ignored `build/pilotllama/`. It does not download,
copy or modify the model. Later starts reuse the stored model selection:

```sh
./scripts/start.sh
./scripts/stop.sh
```

Stop first closes admission and waits briefly for admitted handlers, then terminates
the recorded Go process; the Go lifecycle manager joins its owned native child. The
script validates the recorded PID and refuses to signal an unrelated process. See
the options, security boundaries and verification notes below.

## Inspected environment — 2026-10-16

| Tool / hardware | Observation |
| --- | --- |
| Mac | MacBook Air M4, 10 CPU cores, 10 GPU cores, 24 GB unified memory |
| macOS | 26.6.2, arm64 |
| Disk | Approximately 294 GiB available at inspection |
| Compiler | Apple Clang 17.0.0 |
| Selected developer directory | /Library/Developer/CommandLineTools |
| Selected SDK | macOS 15.5 |
| CMake / Ninja | 4.2.2 / 1.13.2 |
| Go | 1.25.5 |
| Git | 2.39.5 |
| Project Python | User-installed /opt/homebrew/bin/python3.12, version 3.12.15 |
| Apple Python | /usr/bin/python3, version 3.9.6; leave untouched |

Hardware inspection reports Metal support. `xcrun --find metal` fails with the
selected developer tools. Phase 0C subsequently built and ran Metal inference using
upstream's embedded shader path. No toolchain update was needed for that build.

A C++20 syntax probe including thread and stop_token found that std::jthread and
std::stop_token are unavailable in the selected standard library. The CLI does
not require them. Do not enable experimental library flags as an undocumented fix.

## Local C++ scaffold

From the repository root:

```sh
cmake --preset dev
cmake --build --preset dev
./build/dev/inferno --help
./build/dev/inferno --version
cmake --preset release
cmake --build --preset release
./build/release/inferno --version
```

Build products stay under `build/`. Invalid CLI arguments should exit with code 2;
help and version should exit with code 0. Phase 2A adds the C++ unit suite below.

Phase 0B verification on 2026-10-16: Debug and Release configure/build completed
successfully with Apple Clang 17. Both binaries passed smoke checks for no arguments,
help, version, an unknown argument, and excess arguments (10 cases total). Version
output was `inferno 0.1.0`; invalid invocations returned 2 and wrote to stderr.
The isolated Python environment reported 3.12.15 and a distinct virtualenv prefix.
No dependencies were installed and no model weights were downloaded.

## Isolated Python environment

```sh
/opt/homebrew/bin/python3.12 -m venv --without-pip .venv
.venv/bin/python --version
source .venv/bin/activate
```

The environment intentionally contains no pip or third-party packages. Dependency
installation is a separate approval checkpoint. Activation affects only the current
shell; `deactivate` restores it. `.python-version` records the selected version but
does not install Python or change the shell's PATH by itself.

## Pinned llama.cpp build and smoke test

Phase 0C was approved and completed. The initial source acquisition used:

```sh
git clone --depth 1 https://github.com/ggml-org/llama.cpp.git third_party/llama.cpp
git -C third_party/llama.cpp checkout --detach 2a667032dc788c3a071d538e0b572e45a3f12731
```

For reproduction in a fresh project copy, fetch the recorded revision directly
(after source-download approval), rather than assuming today's default branch:

```sh
git init third_party/llama.cpp
git -C third_party/llama.cpp remote add origin https://github.com/ggml-org/llama.cpp.git
git -C third_party/llama.cpp fetch --depth 1 origin 2a667032dc788c3a071d538e0b572e45a3f12731
git -C third_party/llama.cpp checkout --detach 2a667032dc788c3a071d538e0b572e45a3f12731
sh scripts/build-llama.sh
sh scripts/smoke-llama.sh > build/llama-metal/formatted-smoke.stdout.txt 2> build/llama-metal/formatted-smoke.stderr.txt
```

The build script records the full CMake configuration and checks the revision and
clean checkout before compiling. It builds completion and bench tools only, with
four build jobs. It does not fetch dependencies or install targets. No external
TLS, OpenMP, or KleidiAI installation is needed. The source directory is ignored
by the parent project's `.gitignore`; its pin is stored separately.

The smoke script reads the existing [cached model](model-selection.md), runs offline,
and uses a fixed formatted prompt because the cached file has no chat template.
An optional first argument accepts another local path to the same model family.
Do not apply this fixed format to arbitrary architectures. `/usr/bin/time -l` records
macOS process memory and wall time. Logs remain under ignored `build/` output.

The first sandbox test failed to create a GPU command queue. Running the local
binary outside the sandbox was separately approved and succeeded. If this happens
again, request execution approval; do not change GPU settings or disable protections.
See [runtime observations](runtime-internals.md).

Validation commands also run in Phase 0C:

```sh
./build/llama-metal/bin/llama-completion --version
./build/llama-metal/bin/llama-completion --help
./build/llama-metal/bin/llama-bench --help
sh -n scripts/build-llama.sh scripts/smoke-llama.sh
git -C third_party/llama.cpp status --short
```

The upstream checkout remained clean. No upstream unit suite or numerical parity
suite was run; this milestone validates build and one inference workload only.

Do not modify system files, drivers, frameworks, firmware, kernel settings, or
hardware configuration. Never install dependencies or download large weights
without explicit approval. No system-wide installation target is provided here.

## C++ core tests (Phase 2A)

GoogleTest v1.17.0 source was explicitly approved for this project. In a fresh copy,
obtain approval before downloading it, then reproduce its exact pin:

```sh
git init third_party/googletest
git -C third_party/googletest remote add origin https://github.com/google/googletest.git
git -C third_party/googletest fetch --depth 1 origin 52eb8108c5bdec04579160ae17225d66034bd723
git -C third_party/googletest checkout --detach 52eb8108c5bdec04579160ae17225d66034bd723
cmake --preset dev
cmake --build --preset dev -j 4
ctest --test-dir build/dev --output-on-failure
```

The original download used a shallow clone of official tag `v1.17.0`. CMake checks
the recorded commit, builds tests locally, disables GoogleMock and installation,
and never downloads source automatically. Use `-DBUILD_TESTING=OFF` to build only
the core/CLI without GoogleTest. Release uses the `release` preset and build path.
Sanitizer results are not claimed by this project.

## Inferno native generation (Phase 2C)

The native preset imports libraries already produced by `sh scripts/build-llama.sh`.
It validates the source checkout pin and clean state; it does not rebuild or download
upstream. If the library is missing, complete the approved upstream build first.

```sh
cmake --preset native
cmake --build --preset native -j 4
ctest --test-dir build/native -LE model --output-on-failure
./build/native/inferno generate --model /absolute/path/to/existing-model.gguf \
  --prompt "Explain what a CPU does in one short sentence." --max-tokens 64 --stream --metrics
```

After approval for a real-model test, explicitly opt in:

```sh
cmake --preset native -DINFERNO_TEST_MODEL=/absolute/path/to/existing-model.gguf
cmake --build --preset native -j 4
ctest --test-dir build/native -L model --output-on-failure
```

This CMake path is cached; use `-DINFERNO_TEST_MODEL=` to unregister model tests.
Ordinary dev/release CI does not load weights. Native generation and model tests may
need sandbox execution approval for Metal access. No networking or automatic model
downloads are performed. The executable depends on the local upstream library
directory; relocatable packaging is not implemented.

Phase 2D adds optional `--stream` and `--metrics`. The former flushes token bytes to
stdout; the latter prints one request-metrics JSON object to stderr alongside upstream
logs. It includes runtime-event TTFT, excluding model load, and observed token count.
See [observability](observability.md) for exact timing boundaries and null semantics.
The model integration test also checks callback-failure cleanup/recovery.

Phase 2E adds optional `--timeout-ms 1..3600000`. The CLI budget begins after model
load and before submission. A timeout/cancellation returns exit code 1 with a terminal
error; `--metrics` still emits available observations. Cooperative Metal cleanup can
finish after the budget expires. Library callers may use `Submission.control->cancel()`;
no CLI signal handler or automatic disconnect-to-cancellation mapping is installed.

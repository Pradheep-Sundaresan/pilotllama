# Public repository boundary

This document defines what may enter the future public PilotLlama repository. It
does not publish the project or grant a project license.

## Include

- C++, Go, Python, React/TypeScript and Protocol Buffer source.
- Model-free tests and small fixtures created for this project.
- Build, generation, verification and user-space lifecycle scripts.
- Dependency lockfiles, revision pins and generated Go Protocol Buffer bindings
  needed by the current build.
- Architecture, API, setup, measurement and verification documentation.
- Brand assets only after their redistribution rights are recorded.

## Exclude

- GGUF, safetensors and other model weights.
- `build/`, `.venv/`, `.tools/`, `studio/node_modules/`, `studio/dist/` and
  project-local third-party source checkouts.
- Raw benchmark results and logs. Small reviewed summaries already represented in
  source or documentation may be included deliberately.
- Authentication tokens, private keys, environment files and local configuration.
- PID files, sockets, process state, user model registries and request history.
- Machine-specific IDE/CMake files and macOS metadata.

The root `.gitignore` enforces these exclusions before repository initialization.
Large or sensitive files must still be reviewed before every public push; ignore
rules do not protect a file after it has been committed.

## Models

Models remain user-supplied and separately licensed. PilotLlama must not redistribute
weights or imply that its future project license covers a model. The currently
verified native path is the documented Nemotron 4B GGUF. Phase 7B permits the serial
queue path for decoder-only GGUFs whose embedded template is recognized by the pinned
llama.cpp, while failing closed otherwise. No non-Nemotron model has yet been
verified, and current code must not be advertised as supporting arbitrary models.

## Licensing

The source is offered under Apache License 2.0; see the top-level `LICENSE` and
`NOTICE`. Copyright is held by Pradheep Sundaresan. This license covers PilotLlama
source and original project artwork, not user-supplied models or third-party code.

Third-party source is fetched into ignored directories and remains under its own
license. See `THIRD_PARTY_NOTICES.md` and the recorded revisions under `third_party/`.

## Pre-push checklist

1. Confirm the root license, copyright holder, product name and committed brand
   assets remain correct.
3. Initialize Git only when the source tree is ready; this audit does not do so.
4. Review the complete staged file list and staged file sizes.
5. Search staged content for credentials, private keys and absolute user paths.
6. Verify ignored weights, results, dependencies and build products are absent.
7. Run the documented C++, Go, Python, binding and Studio checks.
8. Perform a fresh-checkout setup test before tagging a release.

GitHub publication, remotes, releases and package distribution are separate,
explicit actions.

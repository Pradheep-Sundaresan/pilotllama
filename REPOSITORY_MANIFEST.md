# PilotLlama repository manifest

This is the intended public source boundary for the future GitHub repository.
It is a staging aid only; it does not initialize Git or publish anything.

## Include

- Native C++ runtime, Go control plane, Protocol Buffer contracts, Studio
  source, Python benchmark runners, model-free tests, and build scripts.
- `README.md`, `DESIGN.md`, `LICENSE`, `NOTICE`, and
  `THIRD_PARTY_NOTICES.md`.
- Public developer documentation: setup, API, architecture, model limits,
  lifecycle, scheduling, observability, memory, benchmarking, and diagrams.
- The original PilotLlama mascot at `studio/public/pilotllama-hero.png`.
- Dependency lockfiles and revision pins only; never downloaded dependency
  trees or generated build output.

## Keep local

- `AGENTS.md` and phase-by-phase engineering history. They remain in this
  working directory but are ignored through `.gitignore`.
- Models, benchmark raw results, logs, build products, virtual environments,
  Go/npm caches, process state, local model selection, and credentials.

## Before your manual push

1. Initialize Git only when you are ready.
2. Run `git status --short --ignored` and inspect the staged file list with
   `git diff --cached --name-only`.
3. Confirm no GGUF/model, `build/`, `third_party/`, `.tools/`, `.venv/`,
   `node_modules/`, token, log, local config, or raw result is staged.
4. Run the documented checks in `README.md` and `docs/setup.md`.
5. Review third-party and model licenses independently before distributing a
   compiled binary or a model.

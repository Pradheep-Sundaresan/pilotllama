# Third-party notices

PilotLlama/Inferno uses third-party projects that remain under their respective
licenses. This summary is informational; the dependency lockfiles and source license
texts are authoritative.

## Native dependencies

| Project | Recorded source | License |
| --- | --- | --- |
| llama.cpp | `third_party/llama.cpp-revision.txt` | MIT |
| GoogleTest | `third_party/googletest-revision.txt` | BSD 3-Clause |
| gRPC and its pinned source dependencies | `third_party/grpc-revisions.json` | Apache-2.0 and dependency-specific licenses |

These source checkouts are intentionally ignored and are not part of the future
repository. Fresh setup must fetch the recorded revisions only after the user
approves dependency downloads. A binary distribution must reproduce all notices
required by the exact sources it contains.

## Go dependencies

Exact module versions are recorded in `controlplane/go.mod` and
`controlplane/go.sum`. They include gRPC, Protocol Buffers and supporting Go modules.
Their upstream license texts must be collected and reviewed before distributing
compiled binaries.

## Studio dependencies

Exact npm packages and integrity hashes are recorded in `studio/package-lock.json`.
The direct dependencies include React, Radix UI, Lucide, Recharts, Tailwind CSS and
supporting utilities. `studio/node_modules/` and generated `studio/dist/` output are
not committed. Required browser-bundle notices must be reviewed before release.

## Models and artwork

No model weights are distributed. Each model remains subject to its own license.
The PilotLlama mascot at `studio/public/pilotllama-hero.png` is original project
artwork. Its owner has authorized redistribution as part of this Apache-2.0
repository; see `NOTICE`.

## Project license

A top-level project license has not yet been selected. That choice is a release
blocker and is intentionally not inferred from third-party licenses.

# Memory observability contract — Phase 5A

Phase 5A defined the measurement design; Phase 5B implements its bounded telemetry
without running the model. Neither establishes a safe concurrency limit. The inspected llama.cpp
revision is `2a667032dc788c3a071d538e0b572e45a3f12731` and the inspected macOS SDK is
the existing local Command Line Tools SDK.

## What must be kept separate

Inferno needs two complementary views of memory:

1. **Process accounting from macOS** describes the native server process at the
   instant of a sample and its lifetime peaks. It includes Inferno, gRPC, llama.cpp,
   mapped model pages, runtime buffers, and other resident process pages.
2. **llama.cpp allocation accounting** describes buffers owned by the loaded model
   and context, grouped as model, context, and temporary compute allocations. It is
   an allocation description, not a measurement of physical residency or pressure.

These views overlap. Never add them together. Apple Silicon uses unified memory, so
host and Metal rows are placement/allocation categories rather than independent RAM
and dedicated VRAM pools.

## Metric contract

All byte values are unsigned integer bytes. A failed or unsupported observation is
absent/null with a recorded source status; zero is valid only when the source
actually reports zero.

| Field | Kind | Source and method | Meaning and limits |
| --- | --- | --- | --- |
| `process_resident_bytes` | Measured snapshot | `task_info(mach_task_self(), TASK_VM_INFO, ...)` → `resident_size` | Resident pages charged to the process at the call. Includes mapped model pages; it is not GPU memory. |
| `process_peak_resident_bytes` | Measured lifetime peak | Same call → `resident_size_peak` | Maximum resident size since process start, not since a request or sampling window began. |
| `process_physical_footprint_bytes` | Measured snapshot | Same call → `phys_footprint` | macOS physical-footprint accounting for the process. Its accounting differs from RSS. |
| `process_peak_physical_footprint_bytes` | Measured lifetime peak | Same call → `ledger_phys_footprint_peak`, only when the returned structure revision includes it and the value is nonnegative | Lifetime peak macOS footprint. Keep null when unavailable. |
| `model_tensor_bytes` | Reported allocation | Public `llama_model_size(model)` | Total tensor bytes known to llama.cpp. It is not file size, RSS, or current residency. |
| `llama_buffer_rows[]` | Reported allocation | Pinned WIP `llama_get_memory_breakdown(context)` from `src/llama-ext.h` | One row per upstream buffer type with its name/placement plus `model_bytes`, `context_bytes`, and `compute_bytes`. Rows must be retained rather than relabelled as RAM/VRAM. |
| `llama_model_allocated_bytes` | Derived allocation | Sum of the model column over the returned rows | Logical allocation total; may differ from `model_tensor_bytes` and process accounting. |
| `llama_context_allocated_bytes` | Derived allocation | Sum of the context column over the returned rows | Allocated hybrid attention/recurrent context capacity. Clearing a sequence may not shrink this allocation. |
| `llama_compute_allocated_bytes` | Derived allocation | Sum of the compute column over the returned rows | Allocated temporary compute buffers, not work performed or peak scratch use. |

The breakdown API is explicitly marked WIP by upstream and uses C++ container types.
Inferno may use it only inside the pinned native adapter, behind an original stable
Inferno data structure. Generated RPC and public APIs must never expose upstream C++
types. A pin update must revalidate this adapter. No upstream patch is required for
the initial implementation.

## Nemotron hybrid-state boundary

The inspected Nemotron-H path constructs hybrid memory from an attention cache and
recurrent state. Internally, llama.cpp can calculate each component's allocation and
sums them into the context column returned by `llama_get_memory_breakdown`.

The stable public API does not expose a structured attention-versus-recurrent split.
The Phase 0 smoke log happened to print separate 64.00 MiB attention and 81.09 MiB
recurrent buffers, but parsing diagnostic text would be brittle and must not become
a product metric. Phase 5B will report the combined hybrid-context allocation. The
separate components remain unavailable unless a later, separately approved milestone
can add a small isolated upstream/API adapter with meaningful tests.

Sequence position queries can describe a position range. They do not measure bytes,
occupied physical pages, or cache utilization and must not be presented as memory
consumption.

## Sampling boundaries

Phase 5B takes bounded, synchronous snapshots at meaningful existing safe
points rather than create a polling thread:

| Sample point | Purpose |
| --- | --- |
| `backend_ready` | Process and llama allocation state immediately after the model/context are constructed |
| `request_start` | Process state before prompt work begins |
| `prefill_complete` | Process state after synchronized prefill and before decode |
| `request_complete_before_cleanup` | Process state after decode while request state is still present |
| `request_complete_after_cleanup` | Process state after synchronized sequence/context cleanup |

The macOS lifetime-peak fields capture peaks between snapshots, so per-token polling
is unnecessary. Snapshot calls must happen on the existing owner thread and must not
introduce a detached sampler, unbounded history, or model callback re-entry.

Each snapshot also records its sample-point enum and a monotonic timestamp. Go may
add a separate wall-clock observation time when it retrieves the snapshot. Do not
subtract unrelated current snapshots and label the result as per-request ownership:
model pages, allocator reuse, gRPC, and other process activity are shared.

Keep only the latest snapshot for each sample point plus process lifetime peaks and
the immutable llama allocation breakdown. This bounds native memory telemetry. Do
not attach the full breakdown to every request-history record.

## API placement

Phase 5B extends internal gRPC status with an optional memory snapshot and adds
an authenticated `GET /internal/v1/memory` projection for Studio. Missing native
support remains explicit. The existing aggregate `memory_bytes` field stays null:
it describes a completion window, while these values are point-in-time or
process-lifetime observations and cannot honestly populate that field.

The endpoint should return:

- source status and sample point;
- current and lifetime-peak process measurements;
- model tensor bytes;
- llama allocation totals and buffer rows;
- configured context, batch, sequence, and GPU-layer values needed to interpret the
  allocation;
- measurement timestamp and native status-RPC latency from Go.

No prompt, generated text, model path, credential, or raw error is retained.

## Safety and unavailable claims

macOS exposes current RSS/footprint through `TASK_VM_INFO`, but neither value is a
complete system-memory-pressure signal. Free-page counts are also misleading on a
compressed, cached unified-memory system. Phase 5A therefore defines no invented
"safe percentage" and no automatic kill threshold.

Phase 5C's separately approved native experiment must remain short and opt-in. It
will record the configuration and process snapshots before and after a small request,
use the existing cached model, and observe macOS memory-pressure/thermal diagnostics
without changing them. Allocation failure, model failure, or an adverse pressure or
thermal observation ends the experiment. It will not run a soak test.

Memory values do not establish:

- dedicated GPU/VRAM use on Apple Silicon;
- physical bytes owned by one request;
- live attention versus recurrent allocation;
- available system memory or a universal safe concurrency limit;
- energy, temperature, or performance improvement.

The first two-sequence native batching test remains blocked until the Phase 5
baseline is measured and reviewed.

## Phase 5 milestones

- **5A (complete):** source audit and this measurement contract.
- **5B (complete):** bounded native snapshots, internal gRPC transport, authenticated
  Go projection, and model-free tests. No model run.
- **5C (complete):** short cached-model baseline and cleanup/reuse check.
- **5D:** Memory Lab using the real 5C evidence and live snapshots, with JSON/CSV
  export and explicit unavailable labels.

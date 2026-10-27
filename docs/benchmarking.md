# Baseline benchmark methodology

## Continuous DFlash comparison

The public concurrency runner can compare the same qualified continuous target with
and without an explicit existing DFlash draft:

```sh
python3.12 benchmarks/runners/batching_comparison.py /absolute/path/target.gguf \
  --draft-model /absolute/path/draft.gguf \
  --config benchmarks/workloads/dflash-continuous.json
```

The bounded M4 Air closeout used one warmup followed by two concurrent clients with
two 96-token repetitions each. Both modes completed 4/4 requests, reached two native
executing sequences and produced identical output digest sets. Target-only measured
16.53 tok/s aggregate throughput and 218.65 ms median client TTFT. DFlash measured
15.96 tok/s, 750.82 ms TTFT and accepted 240 of 980 drafted tokens (24.49%). Median
client latency was 11,446.39 ms target-only and 10,437.30 ms with DFlash.

This four-request sample demonstrates correctness and telemetry, not a general speedup,
tail-latency or thermal claim. Raw JSON/CSV results stay ignored under
`benchmarks/results/`; the runner records target/draft identity, runtime revisions,
configuration, request correlations, scheduler samples and memory snapshots.

Phase 4's public-API concurrency harness and its distinct client/scheduler metric
boundaries are documented in [concurrency-benchmarking.md](concurrency-benchmarking.md).

Phase 1A uses `llama-bench` from the pinned upstream checkout, wrapped by Python
3.12 standard-library code. It runs on native Apple Silicon macOS with the existing
Nemotron Q4_K_M artifact. No Python packages or weights need downloading.

```sh
.venv/bin/python -m unittest discover -s benchmarks/tests -v
.venv/bin/python benchmarks/runners/upstream_baseline.py --model /absolute/path/to/existing-model.gguf
```

Optional arguments: `--config` and `--output`; `--model` is required and must name an
existing GGUF. Output must stay inside the project and must not already exist. Default
results go into a unique UTC-named directory under `benchmarks/results/`, excluded
from Git. Do not execute a new experiment beyond an approved milestone without
checking usage and requesting approval. GPU access may need sandbox escalation; no
system settings should be changed.

Phase 1B adds a decode-depth workload:

```sh
.venv/bin/python benchmarks/runners/upstream_baseline.py --model /absolute/path/to/existing-model.gguf --config benchmarks/workloads/decode-depth.json
```

`decode_depths` is optional (default `[0]`). The versioned depth workload uses 0,
512, and 2048 tokens of existing context before 32 timed decode steps, five samples
each. An empty prompt list skips prefill cases. At least one workload is required;
lists are bounded to eight values, and decode depth plus steps is capped at 4096.
JSON schema version 2 and CSV expose `context_depth_tokens`. Old baseline JSON
remains readable; its absent summary depth field means zero, confirmed by raw
upstream `n_depth`.

## Workloads and controls

`benchmarks/workloads/baseline.json` specifies 128/512-token prompt processing and
32-step decode, each with five measured repetitions. Cases run sequentially in
separate processes, with one sequence, four CPU threads, batch/microbatch 128,
99 requested GPU layers, polling disabled, and upstream warmup enabled. Runtime
defaults not explicitly overridden are preserved in each upstream JSON row; CMake
settings, exact argv, binary hash, and full upstream revision are recorded too.

Upstream generates synthetic token IDs using its C library PRNG. Decode evaluates
one synthetic token at a time rather than sampling meaningful text or stopping on
EOS. There is no exposed seed option in this pinned benchmark; no cross-platform
token-stream reproducibility claim is made. This is an execution microbenchmark,
not a model quality or chat test. The cached model's missing chat template does not
affect these synthetic workloads.

Source inspected: `tools/llama-bench/llama-bench.cpp` at the project pin. `test_prompt`
processes batches and synchronizes before returning. `test_gen` synchronizes after
each step. Per repetition, upstream clears model memory before timing; depth is zero.
Prompt warmup processes the full prompt; decode warmup processes one token. Model
load, context creation, warmup, and state clearing are outside the measured samples.

The baseline's requested context is prompt + generation + depth, so 128, 512, and 32
respectively. The decode-depth workload requests 32, 544, and 2080. Runtime allocation
may round capacity up; `context_tokens_requested`
is not a measured allocation. These results are not directly comparable with the
4096-context Phase 0C smoke check. These depth cases vary both occupied context and
requested capacity; they do not isolate those effects. Fixed-capacity experiments
remain future work. Run equivalent workloads with identical settings when comparing
changes; avoid comparing unrelated model architectures as proof of runtime quality.

## Metric definitions

| Metric | Source / unit | Method |
| --- | --- | --- |
| Mean prefill/decode rate | `samples_ns`, tokens/s | Arithmetic mean of tokens * 1e9 / duration for each repetition |
| Rate variability | Derived, tokens/s | Sample standard deviation of the per-repetition rates (N-1 denominator) |
| Mean measured duration | `samples_ns`, ms | Arithmetic mean of upstream sample durations / 1e6 |
| Process wall time | Python monotonic clock, seconds | Wrapper spawn through completion; includes load/warmup/exit and wrapper overhead |
| Peak process RSS | macOS `/usr/bin/time -l`, bytes | Maximum resident set size for the child command, including setup/warmup |
| Requested context | Upstream configuration, tokens | Prompt + decode + depth; not actual allocated capacity |
| Existing context depth | Upstream `n_depth`, tokens | Synthetic prefix evaluated/restored before timed work |

For nonzero depth, upstream first evaluates the synthetic prefix and snapshots its
sequence state. Later repetitions restore that state (or reevaluate if restoration
fails). Both prefix preparation and restoration are outside sample timing but inside
process wall time and peak RSS. The rate numerator counts only the 32 timed steps,
never the existing prefix. RSS at depth includes state-snapshot storage as well as
runtime buffers, so RSS growth is not a direct measurement of KV-cache growth.

macOS RSS includes resident mapped pages and is not a separate GPU-memory metric.
Missing RSS is JSON null, never zero. Each case's peak covers that process's entire
run, not an individual repetition. No steady-state memory claim is made.

TTFT, inter-token latency, request P50/P95/P99, queue waiting/depth, error rate under
load, and aggregate concurrent throughput are unavailable in Phase 1A. Do not derive
them from these rates. Five repetitions are not enough to characterize tail latency.

## Saved evidence and limitations

- `results.json`: status, timestamps, host, model identity, runtime/build config,
  exact commands, raw upstream rows, samples, and summarized metrics.
- `summary.csv`: compact measured/derived metrics; full metadata remains in JSON.
- Each case directory: `command.json`, raw `stdout.json`, and `stderr.log`.
- Failed cases retain their evidence and a failed manifest; the runner never retries
  or silently substitutes CPU execution or another model. Timeout stops the child
  process group. Inspect upstream backend/device fields before claiming GPU results.

The runner validates workload counts, sample lengths, positive timings, rates, and
binary build commit against the pin. It rejects modified upstream source. Unit tests
use tiny synthetic data and subprocesses, without model access.

Keep other workloads quiet and use consistent power and cooling conditions. This
first run does not control or measure temperature, power state, OS cache residency,
or all background activity. Reproducibility means configurations and evidence are
preserved; identical timings are not promised. Model source revision is unknown for
the cached artifact, so the exact weight hash is recorded and checked.

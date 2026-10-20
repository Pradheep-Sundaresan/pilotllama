# Initial upstream baseline — 2026-10-17 IST

Phase 1A completed one sequential experiment with five repetitions per case.
Run ID: `20261016T194053.801847Z` (UTC). All three cases completed successfully.
This establishes an initial reference; no Inferno optimization has been applied.

| Case | Mean tokens/s | Sample standard deviation, tokens/s | Peak process RSS, bytes |
| --- | ---: | ---: | ---: |
| Prefill, 128 tokens | 299.07 | 1.19 | 3,092,807,680 |
| Prefill, 512 tokens | 295.84 | 1.00 | 3,094,642,688 |
| Decode, 32 synthetic steps | 26.33 | 0.63 | 3,081,895,936 |

Configuration: Apple M4, 24 GB unified memory, macOS 26.6.2 arm64, Python 3.12.15;
upstream reports `MTL,BLAS` and Apple M4. CMake Release build, Apple Clang 17,
embedded Metal shaders, four threads, batch/microbatch 128, GPU-layer request 99,
one sequence, depth zero, polling disabled, default warmup. Requested contexts are
128, 512, and 32 tokens respectively, before any runtime allocation rounding.

Runtime revision: `2a667032dc788c3a071d538e0b572e45a3f12731`.
Model: cached NVIDIA Nemotron 3 Nano 4B, Q4_K_M, 2,837,586,496 bytes.
Model SHA-256: `527db2cf6c705d8fabb95693d038d9c06b4a2b0b8b0a4bbdbd01212d37242970`.
Original model/conversion revision is unknown; the artifact hash is verified.

See the archived run's raw JSON for individual samples and the exact binary hash.
Local evidence is in `benchmarks/results/20261016T194053.801847Z/`: `results.json`,
`summary.csv`, and case directories with command, raw JSON, and stderr/time logs.
Large/raw result directories remain excluded from Git; this compact report is kept.

Use the [methodology](benchmarking.md) and versioned workload to repeat the run.
Synthetic decode steps are not sampled chat output. Rates exclude model loading,
warmup, and other setup. RSS covers the entire child process, including setup;
it does not isolate GPU memory. The mean rate is the mean of per-sample rates.
This first run's wrapper wall timer also included parsing/summarization overhead;
the runner now stops that timer immediately after child completion. Upstream sample
durations and the reported rates were unaffected.

This small experiment did not control power, thermal state, or background activity.
It does not measure request latency, TTFT, concurrency, steady-state memory, model
quality, or performance improvements. The detailed historical evidence for the
subsequent independent repeat and depth experiment. These remain preliminary
measurements rather than a distribution of independent runs.

Phase 1A concepts: prefill versus single-token decode, synchronized GPU timings,
warmup boundaries, arithmetic means of rates, variability, process memory accounting,
and preserving artifact/build/workload identity for fair comparisons.

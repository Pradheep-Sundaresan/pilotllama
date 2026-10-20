export type BatchingCase = {
  mode: "Queue" | "Continuous";
  concurrency: 1 | 2;
  requests: number;
  completed: number;
  throughput: number;
  ttftMs: number;
  latencyMs: number;
  queueWaitMs: number;
  maxWaiting: number;
  maxExecuting: number;
  schedulerSamples: number;
};

export type BatchingTransition = { offsetMs: number; waiting: number; executing: number };

export const phase6cReport = {
  id: "phase-6c-20261019",
  capturedAt: "2026-10-19T07:57:04.779Z",
  resultPath: "benchmarks/results/batching-20261019T075704.778649Z/",
  hardware: "Apple M4 · 24 GiB unified memory",
  model: "NVIDIA Nemotron 3 Nano 4B · Q4_K_M",
  maxOutputTokens: 64,
  warmupsPerMode: 1,
  schedulerPollIntervalMs: 50,
  outputSha256: "daf98ee177ca44f6073ae5c5c1e6f4bac4874e3eb65266fcede27470ec02c318",
  cases: [
    { mode: "Queue", concurrency: 1, requests: 2, completed: 2, throughput: 22.5160246336, ttftMs: 137.2155, latencyMs: 1044.28225, queueWaitMs: 0.003208, maxWaiting: 0, maxExecuting: 1, schedulerSamples: 41 },
    { mode: "Queue", concurrency: 2, requests: 4, completed: 4, throughput: 23.0724902193, ttftMs: 1146.735709, latencyMs: 2054.614, queueWaitMs: 1012.505959, maxWaiting: 1, maxExecuting: 1, schedulerSamples: 78 },
    { mode: "Continuous", concurrency: 1, requests: 2, completed: 2, throughput: 22.7435757036, ttftMs: 129.77975, latencyMs: 1034.997084, queueWaitMs: 0.006083, maxWaiting: 0, maxExecuting: 1, schedulerSamples: 40 },
    { mode: "Continuous", concurrency: 2, requests: 4, completed: 4, throughput: 22.0372213609, ttftMs: 262.298625, latencyMs: 2164.089125, queueWaitMs: 0.0115, maxWaiting: 0, maxExecuting: 2, schedulerSamples: 82 }
  ] satisfies BatchingCase[],
  memory: {
    queue: { peakRssBytes: 3156508672, peakFootprintBytes: 316410808, contextBytes: 152135680 },
    continuous: { peakRssBytes: 3244195840, peakFootprintBytes: 404048872, contextBytes: 237162496 }
  },
  systemMemoryFree: { beforePercent: 52, afterPercent: 51 },
  thermal: null,
  transitions: {
    queue: [
      { offsetMs: 10, waiting: 0, executing: 0 }, { offsetMs: 63, waiting: 1, executing: 1 },
      { offsetMs: 2096, waiting: 0, executing: 1 }, { offsetMs: 2146, waiting: 1, executing: 1 },
      { offsetMs: 3168, waiting: 0, executing: 1 }, { offsetMs: 4151, waiting: 0, executing: 0 }
    ],
    continuous: [
      { offsetMs: 17, waiting: 0, executing: 0 }, { offsetMs: 60, waiting: 0, executing: 2 },
      { offsetMs: 4351, waiting: 0, executing: 0 }
    ]
  } satisfies Record<"queue" | "continuous", BatchingTransition[]>
};

export const phase6cCsv = `mode,concurrency,requests,completed,throughput_tokens_per_second,ttft_p50_ms,latency_p50_ms,native_queue_wait_p50_ms,max_waiting,max_executing,scheduler_samples,completed_output_sha256
queue,1,2,2,22.5160246336,137.2155,1044.28225,0.003208,0,1,41,daf98ee177ca44f6073ae5c5c1e6f4bac4874e3eb65266fcede27470ec02c318
queue,2,4,4,23.0724902193,1146.735709,2054.614,1012.505959,1,1,78,daf98ee177ca44f6073ae5c5c1e6f4bac4874e3eb65266fcede27470ec02c318
continuous,1,2,2,22.7435757036,129.77975,1034.997084,0.006083,0,1,40,daf98ee177ca44f6073ae5c5c1e6f4bac4874e3eb65266fcede27470ec02c318
continuous,2,4,4,22.0372213609,262.298625,2164.089125,0.0115,0,2,82,daf98ee177ca44f6073ae5c5c1e6f4bac4874e3eb65266fcede27470ec02c318
`;

export type ConcurrencyCase = {
  concurrency: number;
  attempts: number;
  completed: number;
  clientTtftP50Ms: number;
  clientLatencyP50Ms: number;
  nativeQueueWaitP50Ms: number;
  throughputTokensPerSecond: number;
  outputTokens: number;
  schedulerSamples: number;
  maxWaiting: number;
  maxExecuting: number;
  serverBusy: number;
  rateLimited: number;
};

export type SchedulerTransition = {
  offsetMs: number;
  waiting: number;
  executing: number;
};

export const phase4cReport = {
  id: "phase-4c-20261017",
  title: "Single-worker concurrency baseline",
  capturedAt: "2026-10-17T12:56:19.244215Z",
  resultPath: "benchmarks/results/concurrency-20261017T125619.202810Z/",
  hardware: "Apple M4 · 24 GiB unified memory · 10 logical CPUs",
  os: "macOS 26.6.2 arm64",
  model: "NVIDIA Nemotron 3 Nano 4B · Q4_K_M",
  modelSha256: "527db2cf6c705d8fabb95693d038d9c06b4a2b0b8b0a4bbdbd01212d37242970",
  llamaRevision: "2a667032dc788c3a071d538e0b572e45a3f12731",
  prompt: "In one sentence, explain why time to first token matters for interactive AI.",
  maxOutputTokens: 64,
  actualOutputTokensPerCompletion: 24,
  warmupRequests: 1,
  schedulerPollIntervalMs: 50,
  runtime: {
    workers: 1,
    waitingCapacity: 2,
    publicSlots: 4,
    contextTokens: 4096,
    batchSize: 128,
    threads: 4,
    gpuLayersRequested: 99,
    ratePerMinute: 60,
    rateBurst: 4
  },
  availability: {
    p95: null,
    p99: null,
    nativePeakRssBytes: null
  },
  cases: [
    { concurrency: 1, attempts: 3, completed: 3, clientTtftP50Ms: 134.280916,
      clientLatencyP50Ms: 1039.093875, nativeQueueWaitP50Ms: 0.006,
      throughputTokensPerSecond: 22.723949256445444, outputTokens: 72,
      schedulerSamples: 59, maxWaiting: 0, maxExecuting: 1, serverBusy: 0, rateLimited: 0 },
    { concurrency: 2, attempts: 6, completed: 6, clientTtftP50Ms: 1147.419667,
      clientLatencyP50Ms: 2051.348458, nativeQueueWaitP50Ms: 1010.08025,
      throughputTokensPerSecond: 23.18483380197423, outputTokens: 144,
      schedulerSamples: 116, maxWaiting: 1, maxExecuting: 1, serverBusy: 0, rateLimited: 0 },
    { concurrency: 4, attempts: 12, completed: 9, clientTtftP50Ms: 2177.292584,
      clientLatencyP50Ms: 3078.858167, nativeQueueWaitP50Ms: 2041.912875,
      throughputTokensPerSecond: 23.069721695695225, outputTokens: 216,
      schedulerSamples: 175, maxWaiting: 2, maxExecuting: 1, serverBusy: 1, rateLimited: 2 }
  ] satisfies ConcurrencyCase[],
  schedulerTransitions: {
    1: [
      { offsetMs: 13, waiting: 0, executing: 0 },
      { offsetMs: 65, waiting: 0, executing: 1 },
      { offsetMs: 2108, waiting: 0, executing: 0 },
      { offsetMs: 2166, waiting: 0, executing: 1 }
    ],
    2: [
      { offsetMs: 10, waiting: 0, executing: 0 },
      { offsetMs: 62, waiting: 1, executing: 1 },
      { offsetMs: 3117, waiting: 0, executing: 1 },
      { offsetMs: 3169, waiting: 1, executing: 1 },
      { offsetMs: 4143, waiting: 0, executing: 1 },
      { offsetMs: 4193, waiting: 1, executing: 1 },
      { offsetMs: 5204, waiting: 0, executing: 1 }
    ],
    4: [
      { offsetMs: 12, waiting: 0, executing: 0 },
      { offsetMs: 58, waiting: 2, executing: 1 },
      { offsetMs: 2104, waiting: 1, executing: 1 },
      { offsetMs: 2158, waiting: 2, executing: 1 },
      { offsetMs: 3127, waiting: 1, executing: 1 },
      { offsetMs: 3180, waiting: 2, executing: 1 },
      { offsetMs: 4151, waiting: 1, executing: 1 },
      { offsetMs: 4212, waiting: 2, executing: 1 },
      { offsetMs: 6228, waiting: 1, executing: 1 },
      { offsetMs: 6283, waiting: 2, executing: 1 },
      { offsetMs: 7248, waiting: 1, executing: 1 },
      { offsetMs: 8321, waiting: 0, executing: 1 }
    ]
  } satisfies Record<number, SchedulerTransition[]>
};

export const phase4cCsv = `concurrency,attempts,completed,client_ttft_p50_ms,client_latency_p50_ms,native_queue_wait_p50_ms,completion_attributed_throughput_tokens_per_second,scheduler_samples,max_waiting,server_busy,rate_limited
1,3,3,134.280916,1039.093875,0.006,22.723949256445444,59,0,0,0
2,6,6,1147.419667,2051.348458,1010.08025,23.18483380197423,116,1,0,0
4,12,9,2177.292584,3078.858167,2041.912875,23.069721695695225,175,2,1,2
`;

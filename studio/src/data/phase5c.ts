export const phase5cReport = {
  id: "phase-5c-20261018",
  capturedAt: "2026-10-18T18:07:48.262348Z",
  resultPath: "benchmarks/results/memory-20261018T180745/results.json",
  model: "NVIDIA Nemotron 3 Nano 4B · Q4_K_M",
  modelAlias: "local-nemotron-4b",
  modelFileBytes: 2837586496,
  requests: 2,
  outputPerRequest: "4",
  allocation: { model: 2829187200, context: 152135680, compute: 82059360 },
  observations: [
    { stage: "Backend ready", resident: 3053469696, footprint: 260131744, peakResident: 3053469696, peakFootprint: 260131744 },
    { stage: "Request 1 cleanup", resident: 3066806272, footprint: 312085408, peakResident: 3066806272, peakFootprint: 312085408 },
    { stage: "Request 2 cleanup", resident: 3066920960, footprint: 312101792, peakResident: 3066920960, peakFootprint: 312134560 }
  ],
  postRunMemoryFreePercent: 53,
  thermalReading: null,
  configuration: { contextTokens: 4096, batchTokens: 128, sequences: 1, gpuLayers: 99 }
} as const;

export const phase5cCsv = `stage,resident_bytes,physical_footprint_bytes,peak_resident_bytes,peak_physical_footprint_bytes
Backend ready,3053469696,260131744,3053469696,260131744
Request 1 cleanup,3066806272,312085408,3066806272,312085408
Request 2 cleanup,3066920960,312101792,3066920960,312134560
`;

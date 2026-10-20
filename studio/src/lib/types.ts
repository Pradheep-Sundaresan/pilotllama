export type Distribution = {
  samples: number;
  p50: number | null;
  p95: number | null;
  p99: number | null;
};

export type AggregateMetrics = {
  window_start_unix_ms: number;
  window_end_unix_ms: number;
  window_duration_ms: number;
  history_capacity: number;
  retention_at_capacity: boolean;
  retained_records: number;
  samples: number;
  completed: number;
  failed: number;
  delivery_failed: number;
  completed_native_output_tokens: number;
  completed_native_tokens_per_second: number | null;
  handler_latency_ms: Distribution;
  rpc_latency_ms: Distribution;
  rpc_ttft_ms: Distribution;
  native_runtime_latency_ms: Distribution;
  native_ttft_ms: Distribution;
  native_decode_tokens_per_second: Distribution;
  queue_depth: number | null;
  memory_bytes: number | null;
};

export type NativeMeasurements = {
  output_tokens: number;
  runtime_latency_ms: number | null;
  queue_wait_ms: number | null;
  backend_execution_ms: number | null;
  prefill_phase_ms: number | null;
  decode_phase_ms: number | null;
  ttft_ms: number | null;
  mean_inter_token_ms: number | null;
  decode_tokens_per_second: number | null;
};

export type RequestRecord = {
  request_id: string;
  finished_at_unix_ms: number;
  outcome: "completed" | "failed" | "delivery_failed";
  handler_latency_ms: number;
  rpc_latency_ms: number | null;
  rpc_ttft_ms: number | null;
  received_tokens: number;
  token_bytes: number;
  receive_tokens_per_second: number | null;
  rpc_code: string | null;
  transport_complete: boolean;
  native_state: string | null;
  native: NativeMeasurements | null;
  stream_write_failed: boolean | null;
};

export type RequestHistory = { capacity: number; records: RequestRecord[] };

export type Admission = {
  state: "accepting" | "draining" | "drained";
  active_generation_handlers: number;
  native_idle: null;
};

export type BackendStatus = {
  state: "external" | "stopped" | "starting" | "ready" | "stopping" | "failed";
  managed: boolean;
  admission: Admission;
};

export type SchedulerReport = {
  observed_at_unix_ms: number;
  source: "cpp_runtime_status_snapshot";
  unit: "requests";
  backend_state: "ready" | "no_model" | "loading" | "draining" | "failed" | "unknown";
  waiting_requests: number;
  executing_requests: number;
  queue_capacity: number;
  status_rpc_latency_ms: number;
};

export type ProcessMemorySample = {
  point: "backend_ready" | "request_start" | "prefill_complete" | "request_complete_before_cleanup" | "request_complete_after_cleanup" | "unknown";
  status: "measured" | "unavailable";
  monotonic_ns: number;
  resident_bytes: number | null;
  peak_resident_bytes: number | null;
  physical_footprint_bytes: number | null;
  peak_physical_footprint_bytes: number | null;
};

export type MemoryReport = {
  observed_at_unix_ms: number;
  source: "cpp_backend_memory_snapshot";
  unit: "bytes";
  backend_state: string;
  status_rpc_latency_ms: number;
  process_samples: ProcessMemorySample[];
  model_tensor_bytes: number | null;
  llama_buffers: { name: string; model_bytes: number; context_bytes: number; compute_bytes: number }[];
  llama_model_allocated_bytes: number | null;
  llama_context_allocated_bytes: number | null;
  llama_compute_allocated_bytes: number | null;
  configuration: { context_tokens: number; batch_tokens: number; sequences: number; gpu_layers: number };
};

export type ModelRecord = {
  id: string;
  object: "model";
  created: null;
  owned_by: "inferno";
};

export type ModelConfig = {
  path: string;
  file_name: string;
  model_id: string;
  batching: "queue" | "continuous";
  size_bytes: number;
  format: "GGUF";
  preflight_level: "container_header_only";
  restart_needed: boolean;
};

export type Health = { healthy?: boolean; ready?: boolean; reason?: string };

export type ApiSnapshot = {
  health: Health | null;
  ready: Health | null;
  backend: BackendStatus | null;
  admission: Admission | null;
  metrics: AggregateMetrics | null;
  history: RequestHistory | null;
  scheduler: SchedulerReport | null;
  memory: MemoryReport | null;
  models: ModelRecord[];
  modelConfig: ModelConfig | null;
  error: string | null;
  refreshedAt: number | null;
};

export type StreamResult = { text: string; requestID: string | null };

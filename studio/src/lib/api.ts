import type {
  Admission,
  AggregateMetrics,
  ApiSnapshot,
  BackendStatus,
  Health,
  MemoryReport,
  ModelCapabilities,
  ModelConfig,
  RequestHistory,
  SchedulerReport,
  StreamResult,
  ToolCall
} from "./types";

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string
  ) {
    super(message);
  }
}

function headers(token: string, json = false): HeadersInit {
  return {
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
    ...(json ? { "Content-Type": "application/json" } : {})
  };
}

async function explainError(response: Response) {
  let body: { error?: { message?: string; code?: string } } | null = null;
  try {
    body = await response.json();
  } catch {
    // The server intentionally returns small sanitized errors; a malformed body is generic.
  }
  throw new ApiError(
    body?.error?.message ?? `Request failed with HTTP ${response.status}`,
    response.status,
    body?.error?.code
  );
}

export async function requestJSON<T>(path: string, token = "", init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: { ...headers(token, init?.body != null), ...init?.headers },
    cache: "no-store"
  });
  if (!response.ok) await explainError(response);
  return response.json() as Promise<T>;
}

async function settled<T>(promise: Promise<T>): Promise<T | null> {
  try {
    return await promise;
  } catch {
    return null;
  }
}

export async function loadSnapshot(token: string): Promise<ApiSnapshot> {
  const [health, ready, backend, admission, metrics, history, scheduler, memory, models, modelConfig, modelCapabilities] = await Promise.all([
    settled(requestJSON<Health>("/healthz")),
    settled(requestJSON<Health>("/readyz")),
    settled(requestJSON<BackendStatus>("/internal/v1/backend", token)),
    settled(requestJSON<Admission>("/internal/v1/admission", token)),
    settled(requestJSON<AggregateMetrics>("/internal/v1/metrics", token)),
    settled(requestJSON<RequestHistory>("/internal/v1/requests", token)),
    settled(requestJSON<SchedulerReport>("/internal/v1/scheduler", token)),
    settled(requestJSON<MemoryReport>("/internal/v1/memory", token)),
    settled(requestJSON<{ data: ApiSnapshot["models"] }>("/v1/models", token)),
    settled(requestJSON<ModelConfig>("/internal/v1/model-config", token)),
    settled(requestJSON<ModelCapabilities>("/internal/v1/model-capabilities", token))
  ]);

  const reachable = health !== null;
  return {
    health,
    ready,
    backend,
    admission,
    metrics,
    history,
    scheduler,
    memory,
    models: models?.data ?? [],
    modelConfig,
    modelCapabilities,
    error: reachable ? null : "PilotLlama API is not reachable at 127.0.0.1:8080.",
    refreshedAt: Date.now()
  };
}

export async function saveModelConfig(token: string, input: { path: string; model_id: string; batching: "queue" | "continuous"; context_tokens: number; max_output_tokens: number }) {
  return requestJSON<ModelConfig>("/internal/v1/model-config", token, { method: "PUT", body: JSON.stringify(input) });
}

export async function applyModelConfig(token: string) {
  return requestJSON<{ state: string; model_id: string }>("/internal/v1/model-config/apply", token, { method: "POST" });
}

export async function control(
  path: "/internal/v1/backend/start" | "/internal/v1/backend/stop" | "/internal/v1/admission/drain" | "/internal/v1/admission/resume",
  token: string
) {
  return requestJSON<BackendStatus | Admission>(path, token, { method: "POST" });
}

type CompletionPayload = {
  model: string;
  messages: { role: "system" | "user" | "assistant"; content: string }[];
  max_tokens: number;
  temperature: number;
  top_p?: number;
  top_k?: number;
  seed?: number;
  stop?: string[];
  n: 1;
  stream: boolean;
  thinking?: boolean;
  tools?: { type: "function"; function: { name: string; description?: string; parameters: Record<string, unknown> } }[];
  tool_choice?: "none" | "auto" | "required";
  response_format?: { type: "json_object" } | { type: "json_schema"; json_schema: { name: string; strict: true; schema: Record<string, unknown> } };
};

export async function complete(
  token: string,
  payload: CompletionPayload,
  onToken: (token: string, channel: "content" | "reasoning") => void,
  signal: AbortSignal
): Promise<StreamResult> {
  const response = await fetch("/v1/chat/completions", {
    method: "POST",
    headers: headers(token, true),
    body: JSON.stringify(payload),
    signal
  });
  if (!response.ok) await explainError(response);
  const requestID = response.headers.get("X-Request-ID");

  if (!payload.stream) {
    const body = (await response.json()) as { choices?: { message?: { content?: string; reasoning_content?: string; tool_calls?: ToolCall[] } }[] };
    const text = body.choices?.[0]?.message?.content ?? "";
    const reasoning = body.choices?.[0]?.message?.reasoning_content ?? "";
    onToken(reasoning, "reasoning");
    onToken(text, "content");
    return { text, reasoning, toolCalls: body.choices?.[0]?.message?.tool_calls ?? [], requestID };
  }

  if (!response.body) throw new ApiError("Streaming response body is unavailable", 502);
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let pending = "";
  let text = "";
  let reasoning = "";
  const toolCalls: ToolCall[] = [];
  while (true) {
    const { done, value } = await reader.read();
    pending += decoder.decode(value, { stream: !done });
    const frames = pending.split("\n\n");
    pending = frames.pop() ?? "";
    for (const frame of frames) {
      const data = frame
        .split("\n")
        .filter((line) => line.startsWith("data:"))
        .map((line) => line.slice(5).trim())
        .join("\n");
      if (!data || data === "[DONE]") continue;
      const event = JSON.parse(data) as {
        choices?: { delta?: { content?: string; reasoning_content?: string; tool_calls?: ({ index: number; id?: string; function?: { name?: string; arguments?: string } })[] } }[];
        error?: { message?: string };
      };
      if (event.error) throw new ApiError(event.error.message ?? "Generation failed", 502);
      const piece = event.choices?.[0]?.delta?.content ?? "";
      const thought = event.choices?.[0]?.delta?.reasoning_content ?? "";
      for (const delta of event.choices?.[0]?.delta?.tool_calls ?? []) {
        toolCalls[delta.index] ??= { id: delta.id ?? "", type: "function", function: { name: "", arguments: "" } };
        const call = toolCalls[delta.index]; if (delta.id) call.id = delta.id; call.function.name += delta.function?.name ?? ""; call.function.arguments += delta.function?.arguments ?? "";
      }
      text += piece;
      reasoning += thought;
      if (thought) onToken(thought, "reasoning");
      if (piece) onToken(piece, "content");
    }
    if (done) break;
  }
  return { text, reasoning, toolCalls: toolCalls.filter(Boolean), requestID };
}

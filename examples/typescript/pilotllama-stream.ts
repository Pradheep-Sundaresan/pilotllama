// Server-side example. Requires a modern runtime with fetch; never ship the token
// to browser JavaScript. Abort `controller` when the downstream client disconnects.
export {};
declare const process: {
  env: Record<string, string | undefined>;
  stdout: { write(value: string): void };
  once(event: "SIGINT", listener: () => void): void;
};

type Message = { role: "system" | "user" | "assistant"; content: string };

const baseURL = process.env.PILOTLLAMA_BASE_URL ?? "http://127.0.0.1:8080/v1";
const apiKey = process.env.PILOTLLAMA_API_KEY;
if (!apiKey) throw new Error("PILOTLLAMA_API_KEY is required");

async function discoverModel(): Promise<string> {
  const response = await fetch(`${baseURL}/models`, {
    headers: { Authorization: `Bearer ${apiKey}` },
  });
  if (!response.ok) throw new Error(`model discovery failed: ${response.status}`);
  const body = (await response.json()) as { data: Array<{ id: string }> };
  if (body.data.length !== 1) throw new Error("expected one loaded model");
  return body.data[0].id;
}

async function streamChat(messages: Message[], signal: AbortSignal): Promise<void> {
  const model = process.env.PILOTLLAMA_MODEL ?? (await discoverModel());
  const response = await fetch(`${baseURL}/chat/completions`, {
    method: "POST",
    signal,
    headers: {
      Authorization: `Bearer ${apiKey}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      model,
      messages,
      max_tokens: 256,
      temperature: 0.7,
      stream: true,
      pilotllama: { include_metrics: true },
    }),
  });

  if (!response.ok || !response.body) {
    const retryAfter = response.headers.get("retry-after");
    throw new Error(`PilotLlama ${response.status}${retryAfter ? `; retry after ${retryAfter}s` : ""}: ${await response.text()}`);
  }

  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  let pending = "";
  let finished = false;
  for (;;) {
    const { value = "", done } = await reader.read();
    pending += value;
    for (;;) {
      const boundary = pending.indexOf("\n\n");
      if (boundary < 0) break;
      const record = pending.slice(0, boundary);
      pending = pending.slice(boundary + 2);
      const data = record.split("\n").find((line) => line.startsWith("data: "))?.slice(6);
      if (!data) continue;
      if (data === "[DONE]") {
        if (!finished) throw new Error("stream ended without a successful finish chunk");
        return;
      }
      const event = JSON.parse(data);
      if (event.error) throw new Error(`${event.error.code}: ${event.error.message}`);
      if (event.object === "pilotllama.metrics") {
        console.error("\nmetrics", event.metrics);
        continue;
      }
      const choice = event.choices?.[0];
      if (choice?.delta?.reasoning_content) console.error(choice.delta.reasoning_content);
      if (choice?.delta?.content) process.stdout.write(choice.delta.content);
      if (choice?.finish_reason) finished = true;
    }
    if (done) throw new Error("stream ended without [DONE]");
  }
}

const controller = new AbortController();
process.once("SIGINT", () => controller.abort());
void streamChat(
  [{ role: "user", content: "Explain continuous batching simply." }],
  controller.signal,
).catch((error: unknown) => {
  throw error;
});

import { useEffect, useRef, useState } from "react";
import { Bot, CircleStop, CornerDownLeft, Radio, Sparkles, TerminalSquare } from "lucide-react";
import { complete } from "../lib/api";
import type { ApiSnapshot, ToolCall } from "../lib/types";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card } from "../components/ui/card";

export function Playground({ snapshot, token, onComplete }: { snapshot: ApiSnapshot; token: string; onComplete: () => void }) {
  const [model, setModel] = useState("");
  const [prompt, setPrompt] = useState("Explain why measuring time to first token separately from total latency matters.");
  const [systemPrompt, setSystemPrompt] = useState("");
  const [history, setHistory] = useState<{ role: "user" | "assistant"; content: string }[]>([]);
  const [maxTokens, setMaxTokens] = useState(128);
  const [stream, setStream] = useState(true);
  const [output, setOutput] = useState("");
  const [reasoning, setReasoning] = useState("");
  const [thinking, setThinking] = useState(false);
  const [temperature, setTemperature] = useState(0);
  const [topP, setTopP] = useState(1);
  const [topK, setTopK] = useState(0);
  const [seed, setSeed] = useState("");
  const [stop, setStop] = useState("");
  const [toolsJSON, setToolsJSON] = useState("");
  const [schemaJSON, setSchemaJSON] = useState("");
  const [toolCalls, setToolCalls] = useState<ToolCall[]>([]);
  const [requestID, setRequestID] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [running, setRunning] = useState(false);
  const controller = useRef<AbortController | null>(null);
  const outputLimit = snapshot.modelCapabilities?.configured_max_output_tokens ?? 512;
  const thinkingSupported = ["detected", "verified"].includes(snapshot.modelCapabilities?.capabilities.thinking ?? "");

  useEffect(() => {
    if (!model && snapshot.models[0]) setModel(snapshot.models[0].id);
  }, [model, snapshot.models]);

  useEffect(() => { setMaxTokens((value) => Math.min(value, outputLimit)); }, [outputLimit]);
  useEffect(() => { if (!thinkingSupported) setThinking(false); }, [thinkingSupported]);

  async function run() {
    if (!model || !prompt.trim()) return;
    controller.current = new AbortController();
    setRunning(true);
    setOutput("");
    setReasoning("");
    setToolCalls([]);
    setError(null);
    setRequestID(null);
    try {
      const messages: { role: "system" | "user" | "assistant"; content: string }[] = [
        ...(systemPrompt ? [{ role: "system" as const, content: systemPrompt }] : []), ...history,
        { role: "user", content: prompt }
      ];
      const tools = toolsJSON.trim() ? JSON.parse(toolsJSON) : undefined;
      const schema = schemaJSON.trim() ? JSON.parse(schemaJSON) : undefined;
      if (tools && schema) throw new Error("Choose tools or structured JSON, not both.");
      const result = await complete(token, {
        model,
        messages,
        max_tokens: maxTokens,
        temperature,
        top_p: topP,
        top_k: topK,
        ...(seed === "" ? {} : { seed: Number(seed) }),
        ...(stop.trim() ? { stop: stop.split(",").map((value) => value.trim()).filter(Boolean).slice(0, 4) } : {}),
        n: 1,
        stream,
        thinking,
        ...(tools ? { tools, tool_choice: "auto" as const } : {}),
        ...(schema ? { response_format: { type: "json_schema" as const, json_schema: { name: "studio_output", strict: true as const, schema } } } : {})
      }, (piece, channel) => channel === "reasoning" ? setReasoning((current) => current + piece) : setOutput((current) => current + piece), controller.current.signal);
      setRequestID(result.requestID);
      setToolCalls(result.toolCalls);
      if (!result.toolCalls.length) setHistory((current) => [...current, { role: "user", content: prompt }, { role: "assistant", content: result.text }]);
      setPrompt("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Generation failed");
    } finally {
      setRunning(false);
      controller.current = null;
      onComplete();
    }
  }

  return (
    <div className="grid min-h-[calc(100vh-9rem)] gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1.05fr)]">
      <Card className="flex flex-col overflow-hidden">
        <div className="border-b border-orange-950/[.07] p-5">
          <div className="flex items-start justify-between gap-4">
            <div><p className="eyebrow">Real inference</p><h2 className="mt-1 text-lg font-semibold text-stone-900">Prompt workspace</h2></div>
            <Badge className={stream ? "border-emerald-400/20 bg-emerald-400/10 text-emerald-700" : ""}>{stream ? "SSE streaming" : "JSON response"}</Badge>
          </div>
        </div>
        <div className="flex flex-1 flex-col gap-5 p-5">
          <label className="space-y-2"><span className="field-label">Loaded model</span><select className="input" value={model} onChange={(event) => setModel(event.target.value)} disabled={running}><option value="">{snapshot.models.length ? "Select a model" : "No ready model"}</option>{snapshot.models.map((item) => <option key={item.id} value={item.id}>{item.id}</option>)}</select></label>
          <label className="space-y-2"><span className="field-label">System message · optional</span><textarea className="input min-h-20 resize-none py-3 leading-relaxed" value={systemPrompt} onChange={(event) => setSystemPrompt(event.target.value)} disabled={running || history.length > 0} placeholder="Define the assistant's behavior for this conversation." /></label>
          <label className="flex flex-1 flex-col space-y-2"><span className="field-label">User message</span><textarea className="input min-h-56 flex-1 resize-none py-4 leading-relaxed" value={prompt} onChange={(event) => setPrompt(event.target.value)} disabled={running} /></label>
          <div className="grid gap-4 sm:grid-cols-2">
            <label className="space-y-2"><span className="field-label">Maximum output tokens</span><input className="input" type="number" min={1} max={outputLimit} value={maxTokens} onChange={(event) => setMaxTokens(Math.max(1, Math.min(outputLimit, Number(event.target.value))))} disabled={running} /></label>
            <label className="space-y-2"><span className="field-label">Transport</span><button type="button" className="input flex items-center justify-between text-left" onClick={() => setStream((value) => !value)} disabled={running}><span>{stream ? "Server-sent events" : "Single JSON response"}</span><span className={`relative h-5 w-9 rounded-full transition ${stream ? "bg-orange-500" : "bg-stone-300"}`}><span className={`absolute top-0.5 size-4 rounded-full bg-white transition ${stream ? "left-[18px]" : "left-0.5"}`} /></span></button></label>
          </div>
          <label className="space-y-2"><span className="field-label">Reasoning stream</span><button type="button" className="input flex items-center justify-between text-left" onClick={() => setThinking((value) => !value)} disabled={running || !thinkingSupported}><span>{thinkingSupported ? (thinking ? "Shown separately" : "Disabled") : "Unavailable for this model"}</span><span className={`relative h-5 w-9 rounded-full transition ${thinking ? "bg-orange-500" : "bg-stone-300"}`}><span className={`absolute top-0.5 size-4 rounded-full bg-white transition ${thinking ? "left-[18px]" : "left-0.5"}`} /></span></button></label>
          <details className="rounded-xl border border-orange-950/[.07] bg-orange-950/[.02] p-4"><summary className="cursor-pointer text-xs font-semibold text-stone-700">Sampling controls</summary><div className="mt-4 grid gap-3 sm:grid-cols-2"><label className="space-y-1"><span className="field-label">Temperature · {temperature}</span><input className="input" type="number" min={0} max={2} step={0.1} value={temperature} onChange={(e) => setTemperature(Number(e.target.value))} disabled={running}/></label><label className="space-y-1"><span className="field-label">Top P</span><input className="input" type="number" min={0.01} max={1} step={0.01} value={topP} onChange={(e) => setTopP(Number(e.target.value))} disabled={running}/></label><label className="space-y-1"><span className="field-label">Top K · 0 disables</span><input className="input" type="number" min={0} max={100000} value={topK} onChange={(e) => setTopK(Number(e.target.value))} disabled={running}/></label><label className="space-y-1"><span className="field-label">Seed · optional</span><input className="input" type="number" min={0} max={4294967295} value={seed} onChange={(e) => setSeed(e.target.value)} disabled={running} placeholder="Random"/></label><label className="space-y-1 sm:col-span-2"><span className="field-label">Stop strings · comma separated, max 4</span><input className="input" value={stop} onChange={(e) => setStop(e.target.value)} disabled={running} placeholder="END, ###"/></label></div></details>
          <details className="rounded-xl border border-orange-950/[.07] bg-orange-950/[.02] p-4"><summary className="cursor-pointer text-xs font-semibold text-stone-700">Tools & structured JSON</summary><div className="mt-4 space-y-3"><label className="space-y-1"><span className="field-label">OpenAI tools array · optional</span><textarea className="input min-h-24 py-3 font-mono text-xs" value={toolsJSON} onChange={(e) => setToolsJSON(e.target.value)} disabled={running || !!schemaJSON.trim()} placeholder='[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}]'/></label><label className="space-y-1"><span className="field-label">JSON Schema · optional</span><textarea className="input min-h-24 py-3 font-mono text-xs" value={schemaJSON} onChange={(e) => setSchemaJSON(e.target.value)} disabled={running || !!toolsJSON.trim()} placeholder='{"type":"object","properties":{"answer":{"type":"string"}}}'/></label><p className="text-xs text-stone-400">PilotLlama returns typed calls; your application executes them.</p></div></details>
          <div className="flex items-center justify-between gap-4 border-t border-orange-950/[.07] pt-5">
            <p className="text-xs text-stone-400">Temperature {temperature} · {history.length / 2} completed turns · 1–{outputLimit.toLocaleString()} tokens</p>
            {history.length ? <Button variant="secondary" onClick={() => { setHistory([]); setOutput(""); setReasoning(""); setRequestID(null); }} disabled={running}>New chat</Button> : null}
            {running ? <Button variant="danger" onClick={() => controller.current?.abort()}><CircleStop size={15} /> Cancel</Button> : <Button onClick={run} disabled={!model || !prompt.trim()}><CornerDownLeft size={15} /> Run inference</Button>}
          </div>
        </div>
      </Card>

      <Card className="flex flex-col overflow-hidden">
        <div className="flex items-center justify-between border-b border-orange-950/[.07] p-5">
          <div className="flex items-center gap-3"><span className="grid size-9 place-items-center rounded-xl bg-orange-400/10 text-orange-700"><Bot size={17} /></span><div><p className="text-sm font-medium text-stone-900">PilotLlama output</p><p className="text-xs text-stone-400">{requestID ? `Request ${requestID}` : "Awaiting generation"}</p></div></div>
          {running ? <span className="flex items-center gap-2 text-xs text-orange-700"><Radio size={13} className="animate-pulse" /> receiving</span> : null}
        </div>
        <div className="relative flex-1 p-6">
          {toolCalls.map((call) => <div key={call.id} className="mb-4 rounded-xl border border-emerald-200 bg-emerald-50/70 p-4"><p className="field-label text-emerald-700">Tool call · {call.function.name}</p><pre className="mt-2 whitespace-pre-wrap text-xs text-stone-700">{call.function.arguments}</pre><p className="mt-2 text-[10px] text-stone-400">{call.id}</p></div>)}
          {history.length || output || reasoning || error ? <div className="space-y-4">{history.map((message, index) => <div key={index} className={message.role === "user" ? "ml-10 rounded-xl bg-orange-50 p-4" : "mr-10 rounded-xl bg-stone-50 p-4"}><p className="mb-2 text-[10px] font-semibold uppercase tracking-wider text-stone-400">{message.role}</p><div className="whitespace-pre-wrap text-[15px] leading-7 text-stone-800">{message.content}</div></div>)}{reasoning ? <div className="mr-10 rounded-xl border border-amber-200 bg-amber-50/70 p-4"><p className="mb-2 text-[10px] font-semibold uppercase tracking-wider text-amber-700">reasoning</p><div className="whitespace-pre-wrap text-sm leading-6 text-stone-600">{reasoning}</div></div> : null}{output && (running || !history.length) ? <div className="mr-10 rounded-xl bg-stone-50 p-4"><p className="mb-2 text-[10px] font-semibold uppercase tracking-wider text-stone-400">assistant</p><div className="whitespace-pre-wrap text-[15px] leading-7 text-stone-800">{output}<span className={running ? "ml-0.5 inline-block h-4 w-1.5 animate-pulse bg-orange-400" : ""}/></div></div> : null}{error ? <div className="rounded-xl border border-red-500/20 bg-red-500/[.07] p-4 text-sm text-red-700">{error}</div> : null}</div> : <div className="grid h-full min-h-72 place-items-center text-center"><div><Sparkles className="mx-auto text-stone-400" size={25} /><p className="mt-4 text-sm font-medium text-stone-600">Output will appear here</p><p className="mt-1 text-xs text-stone-400">Streaming pieces are rendered as the backend sends them.</p></div></div>}
        </div>
        <div className="flex items-center gap-2 border-t border-orange-950/[.07] bg-orange-950/[.025] px-5 py-3 text-[11px] text-stone-400"><TerminalSquare size={13} /> Values in Overview refresh from the server after completion.</div>
      </Card>
    </div>
  );
}

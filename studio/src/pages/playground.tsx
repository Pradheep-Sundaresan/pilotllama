import { useEffect, useRef, useState } from "react";
import { Bot, CircleStop, CornerDownLeft, Radio, Sparkles, TerminalSquare } from "lucide-react";
import { complete } from "../lib/api";
import type { ApiSnapshot } from "../lib/types";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card } from "../components/ui/card";

export function Playground({ snapshot, token, onComplete }: { snapshot: ApiSnapshot; token: string; onComplete: () => void }) {
  const [model, setModel] = useState("");
  const [prompt, setPrompt] = useState("Explain why measuring time to first token separately from total latency matters.");
  const [maxTokens, setMaxTokens] = useState(128);
  const [stream, setStream] = useState(true);
  const [output, setOutput] = useState("");
  const [requestID, setRequestID] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [running, setRunning] = useState(false);
  const controller = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!model && snapshot.models[0]) setModel(snapshot.models[0].id);
  }, [model, snapshot.models]);

  async function run() {
    if (!model || !prompt.trim()) return;
    controller.current = new AbortController();
    setRunning(true);
    setOutput("");
    setError(null);
    setRequestID(null);
    try {
      const result = await complete(token, {
        model,
        messages: [{ role: "user", content: prompt }],
        max_tokens: maxTokens,
        temperature: 0,
        n: 1,
        stream
      }, (piece) => setOutput((current) => current + piece), controller.current.signal);
      setRequestID(result.requestID);
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
          <label className="flex flex-1 flex-col space-y-2"><span className="field-label">User message</span><textarea className="input min-h-56 flex-1 resize-none py-4 leading-relaxed" value={prompt} onChange={(event) => setPrompt(event.target.value)} disabled={running} /></label>
          <div className="grid gap-4 sm:grid-cols-2">
            <label className="space-y-2"><span className="field-label">Maximum output tokens</span><input className="input" type="number" min={1} max={512} value={maxTokens} onChange={(event) => setMaxTokens(Math.max(1, Math.min(512, Number(event.target.value))))} disabled={running} /></label>
            <label className="space-y-2"><span className="field-label">Transport</span><button type="button" className="input flex items-center justify-between text-left" onClick={() => setStream((value) => !value)} disabled={running}><span>{stream ? "Server-sent events" : "Single JSON response"}</span><span className={`relative h-5 w-9 rounded-full transition ${stream ? "bg-orange-500" : "bg-stone-300"}`}><span className={`absolute top-0.5 size-4 rounded-full bg-white transition ${stream ? "left-[18px]" : "left-0.5"}`} /></span></button></label>
          </div>
          <div className="flex items-center justify-between gap-4 border-t border-orange-950/[.07] pt-5">
            <p className="text-xs text-stone-400">Temperature is fixed at 0 · one user message · 1–512 tokens</p>
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
          {output ? <div className="whitespace-pre-wrap text-[15px] leading-7 text-stone-800">{output}<span className={running ? "ml-0.5 inline-block h-4 w-1.5 animate-pulse bg-orange-400" : ""} /></div> : error ? <div className="rounded-xl border border-red-500/20 bg-red-500/[.07] p-4 text-sm text-red-700">{error}</div> : <div className="grid h-full min-h-72 place-items-center text-center"><div><Sparkles className="mx-auto text-stone-400" size={25} /><p className="mt-4 text-sm font-medium text-stone-600">Output will appear here</p><p className="mt-1 text-xs text-stone-400">Streaming pieces are rendered as the backend sends them.</p></div></div>}
        </div>
        <div className="flex items-center gap-2 border-t border-orange-950/[.07] bg-orange-950/[.025] px-5 py-3 text-[11px] text-stone-400"><TerminalSquare size={13} /> Values in Overview refresh from the server after completion.</div>
      </Card>
    </div>
  );
}

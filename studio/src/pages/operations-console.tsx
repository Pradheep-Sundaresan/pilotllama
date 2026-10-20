import { useState } from "react";
import { Activity, Box, BrainCircuit, Cpu, Gauge, MemoryStick, PauseCircle, PlayCircle, Rows3, Server, Settings2, Zap } from "lucide-react";
import { control } from "../lib/api";
import type { ApiSnapshot } from "../lib/types";
import { formatMetric, formatTime, shortID } from "../lib/utils";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card } from "../components/ui/card";
import { RequestInspector } from "./request-inspector";
import { ModelManager } from "./model-manager";
import { SystemHealth } from "./system-health";

function bytes(value: number | null | undefined) {
  if (value == null || !Number.isFinite(value)) return "Unavailable";
  return `${(value / 1024 ** 2).toLocaleString(undefined, { maximumFractionDigits: 1 })} MiB`;
}

function Stat({ label, value, unit }: { label: string; value: string; unit?: string }) {
  return <div className="console-stat"><p>{label}</p><strong>{value}</strong>{unit ? <span>{unit}</span> : null}</div>;
}

export function OperationsConsole({ snapshot, token, refresh }: { snapshot: ApiSnapshot; token: string; refresh: () => Promise<void> }) {
  const [busy, setBusy] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const scheduler = snapshot.scheduler;
  const memory = snapshot.memory;
  const latest = snapshot.history?.records[0];
  const model = snapshot.models[0]?.id ?? "No ready model";
  const saved = snapshot.modelConfig;
  const backend = snapshot.backend;
  const admission = snapshot.admission ?? backend?.admission;
  const process = memory?.process_samples.at(-1);
  const active = Array.from({ length: 2 }, (_, index) => index < (scheduler?.executing_requests ?? 0));
  const waiting = Array.from({ length: scheduler?.queue_capacity ?? 2 }, (_, index) => index < (scheduler?.waiting_requests ?? 0));

  async function action(label: string, path: Parameters<typeof control>[0]) {
    setBusy(label); setMessage(null);
    try { await control(path, token); setMessage(`${label} completed.`); }
    catch (error) { setMessage(error instanceof Error ? error.message : `${label} failed.`); }
    finally { setBusy(null); await refresh(); }
  }

  return <div className="space-y-4">
    <section className="console-frame">
      <div className="console-commandbar">
        <div className="flex min-w-0 items-center gap-3"><span className={`status-beacon ${snapshot.ready?.ready ? "is-ready" : ""}`} /><div className="min-w-0"><p className="truncate text-sm font-semibold text-stone-950">{model}</p><p className="text-[11px] text-stone-500">{backend?.managed ? "Go-managed native runtime" : "Local inference runtime"}</p></div></div>
        <div className="flex flex-wrap items-center justify-end gap-2"><Badge>{saved?.batching ?? "scheduler unavailable"}</Badge><Badge className={admission?.state === "accepting" ? "border-emerald-600/20 bg-emerald-50 text-emerald-700" : "border-amber-600/20 bg-amber-50 text-amber-700"}>{admission?.state ?? "admission unavailable"}</Badge><Button size="sm" variant="secondary" disabled={!token || !!busy} onClick={() => action("Drain", "/internal/v1/admission/drain")}><PauseCircle size={13}/>Drain</Button><Button size="sm" disabled={!token || !!busy} onClick={() => action("Resume", "/internal/v1/admission/resume")}><PlayCircle size={13}/>Resume</Button></div>
      </div>

      <div className="grid xl:grid-cols-[minmax(0,1.55fr)_minmax(320px,.75fr)]">
        <div className="runtime-stage">
          <div className="flex items-start justify-between gap-4"><div><p className="console-kicker">APPLE SILICON RUNTIME</p><h2>Your Mac, running a visible local AI service.</h2><p className="mt-2 max-w-xl text-sm leading-relaxed text-stone-500">Run a local model for your app, then see its requests, waiting work, response speed, memory, and backend health in one place.</p></div><span className="font-mono text-[10px] text-stone-400">127.0.0.1:8080</span></div>
          <div className="runtime-board" aria-label="PilotLlama runtime topology">
            <div className="runtime-chip chip-model"><Box size={17}/><span>GGUF</span><small>{saved?.file_name ?? model}</small></div>
            <span className="runtime-wire wire-a" />
            <div className="runtime-chip chip-control"><Server size={17}/><span>GO</span><small>HTTP · policy · lifecycle</small></div>
            <span className="runtime-wire wire-b" />
            <div className="runtime-chip chip-native"><Cpu size={17}/><span>C++20</span><small>gRPC · scheduler · metrics</small></div>
            <span className="runtime-wire wire-c" />
            <div className="runtime-chip chip-metal"><Zap size={17}/><span>METAL</span><small>llama.cpp execution</small></div>
            <div className="memory-bus"><MemoryStick size={15}/><span>UNIFIED MEMORY</span><b>{bytes(process?.resident_bytes)}</b></div>
            <div className="sequence-bank"><span>ACTIVE SEQUENCES</span><div>{active.map((used, index) => <i key={index} className={used ? "is-active" : ""}>SEQ {index}</i>)}</div></div>
          </div>
          <div className="console-strip">
            <Stat label="RPC TTFT · P50" value={formatMetric(snapshot.metrics?.rpc_ttft_ms.p50)} unit="ms"/>
            <Stat label="Decode · P50" value={formatMetric(snapshot.metrics?.native_decode_tokens_per_second.p50)} unit="tok/s"/>
            <Stat label="Throughput" value={formatMetric(snapshot.metrics?.completed_native_tokens_per_second, 2)} unit="tok/s"/>
            <Stat label="Observed" value={snapshot.metrics ? String(snapshot.metrics.samples) : "Unavailable"} unit="requests"/>
          </div>
        </div>

        <aside className="runtime-inspector">
          <div className="flex items-center justify-between"><div><p className="console-kicker">RUNTIME INSPECTOR</p><h3>{model}</h3></div><BrainCircuit size={20} className="text-stone-400"/></div>
          <dl className="inspector-list"><div><dt>Backend</dt><dd>{backend?.state ?? "Unavailable"}</dd></div><div><dt>Scheduler</dt><dd>{saved?.batching ?? "Unavailable"}</dd></div><div><dt>Context</dt><dd>{memory ? `${memory.configuration.context_tokens.toLocaleString()} tokens` : "Unavailable"}</dd></div><div><dt>Model allocation</dt><dd>{bytes(memory?.llama_model_allocated_bytes)}</dd></div><div><dt>Context allocation</dt><dd>{bytes(memory?.llama_context_allocated_bytes)}</dd></div><div><dt>Physical footprint</dt><dd>{bytes(process?.physical_footprint_bytes)}</dd></div></dl>
          <p className="mt-4 text-[10px] leading-relaxed text-stone-400">Process memory and llama allocations overlap and are intentionally not summed. Model changes apply on the next start.</p>
          {message ? <p className="mt-3 rounded-md bg-stone-100 p-2 text-xs text-stone-600">{message}</p> : null}
        </aside>
      </div>
    </section>

    <div className="grid gap-4 xl:grid-cols-[1.05fr_.95fr]">
      <Card className="overflow-hidden rounded-xl shadow-none">
        <div className="console-panel-head"><div><p className="console-kicker">NATIVE SCHEDULER</p><h3>Bounded execution</h3></div><span className="text-xs text-stone-500">{scheduler?.backend_state ?? "unavailable"}</span></div>
        <div className="scheduler-console"><div><p>Executing</p><div className="slot-row">{active.map((used, index) => <span key={index} className={used ? "active" : ""}><PlayCircle size={13}/>Sequence {index}</span>)}</div></div><div><p>Waiting</p><div className="slot-row">{waiting.map((used, index) => <span key={index} className={used ? "waiting" : ""}><Rows3 size={13}/>Slot {index + 1}</span>)}</div></div></div>
      </Card>

      <Card className="overflow-hidden rounded-xl shadow-none">
        <div className="console-panel-head"><div><p className="console-kicker">LATEST REQUEST</p><h3>{latest ? shortID(latest.request_id) : "No retained request"}</h3></div>{latest ? <Badge>{latest.outcome}</Badge> : null}</div>
        {latest ? <div className="grid grid-cols-2 gap-px bg-stone-200 sm:grid-cols-4"><Stat label="Handler" value={formatMetric(latest.handler_latency_ms)} unit="ms"/><Stat label="Native TTFT" value={formatMetric(latest.native?.ttft_ms)} unit="ms"/><Stat label="Decode" value={formatMetric(latest.native?.decode_tokens_per_second)} unit="tok/s"/><Stat label="Finished" value={formatTime(latest.finished_at_unix_ms)}/></div> : <p className="p-5 text-sm text-stone-500">Run a prompt in Playground to populate request telemetry.</p>}
      </Card>
    </div>

    <div className="console-disclosures">
      <details><summary><Activity size={15}/>Request inspector</summary><div className="disclosure-body"><RequestInspector snapshot={snapshot}/></div></details>
      <details><summary><Settings2 size={15}/>Model configuration</summary><div className="disclosure-body"><ModelManager snapshot={snapshot} token={token} refresh={refresh}/></div></details>
      <details><summary><Gauge size={15}/>Lifecycle and health controls</summary><div className="disclosure-body"><SystemHealth snapshot={snapshot} token={token} refresh={refresh}/></div></details>
    </div>
  </div>;
}

import { useEffect, useState } from "react";
import { AlertTriangle, CheckCircle2, FileCheck2, HardDrive, Info, RefreshCw, RotateCw, Save, ShieldCheck } from "lucide-react";
import type { ApiSnapshot } from "../lib/types";
import { applyModelConfig, saveModelConfig } from "../lib/api";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card } from "../components/ui/card";

export function ModelManager({ snapshot, token, refresh }: { snapshot: ApiSnapshot; token: string; refresh: () => Promise<void> }) {
  const saved = snapshot.modelConfig;
  const capabilities = snapshot.modelCapabilities;
  const [path, setPath] = useState("");
  const [modelID, setModelID] = useState("local-model");
  const [batching, setBatching] = useState<"queue" | "continuous">("queue");
  const [contextTokens, setContextTokens] = useState(4096);
  const [maxOutputTokens, setMaxOutputTokens] = useState(512);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
	const physical = capabilities?.driving_test?.physical_memory_bytes;
	const currentContext = snapshot.memory?.configuration.context_tokens ?? capabilities?.configured_context_tokens ?? 4096;
	const measuredContext = snapshot.memory?.llama_context_allocated_bytes;
	const estimatedContext = measuredContext ? measuredContext * contextTokens / Math.max(1, currentContext) * (batching === "continuous" ? 2 / Math.max(1, snapshot.memory?.configuration.sequences ?? 1) : 1 / Math.max(1, snapshot.memory?.configuration.sequences ?? 1)) : 0;
	const estimatedTotal = (saved?.size_bytes ?? 0) + estimatedContext;
	const memoryRatio = physical && estimatedTotal ? estimatedTotal / physical : null;
  useEffect(() => {
    if (!saved) return;
    setPath(saved.path); setModelID(saved.model_id); setBatching(saved.batching);
    setContextTokens(saved.context_tokens); setMaxOutputTokens(saved.max_output_tokens);
  }, [saved]);

  async function save() {
    setBusy(true); setMessage("");
    try {
      const result = await saveModelConfig(token, { path, model_id: modelID, batching, context_tokens: contextTokens, max_output_tokens: maxOutputTokens });
      setMessage(`${result.file_name} passed GGUF container preflight and is saved for the next start.`);
      await refresh();
    } catch (error) { setMessage(error instanceof Error ? error.message : "Preflight failed"); }
    finally { setBusy(false); }
  }

	async function apply() {
		setBusy(true); setMessage("");
		try { await saveModelConfig(token, { path, model_id: modelID, batching, context_tokens: contextTokens, max_output_tokens: maxOutputTokens }); await applyModelConfig(token); setMessage("Backend restarted in drained mode. Inspect readiness, then use Resume when ready to serve."); await refresh(); }
		catch (error) { setMessage(error instanceof Error ? error.message : "Apply and restart failed"); }
		finally { setBusy(false); }
	}

  return <div className="space-y-5">
    <section className="hero-grid overflow-hidden rounded-2xl border border-orange-400/15 p-6 sm:p-8">
      <Badge className="border-orange-400/20 bg-orange-400/10 text-orange-700">Local models · no downloads</Badge>
      <h2 className="mt-5 text-3xl font-semibold tracking-[-.04em] text-stone-900">Choose what flies next.</h2>
      <p className="mt-3 max-w-2xl text-sm leading-relaxed text-stone-600">Select an existing GGUF, choose safe runtime budgets, check qualification and apply the complete serving configuration from one place.</p>
    </section>

    <div className="grid gap-4 xl:grid-cols-[1.25fr_.75fr]">
      <Card className="p-5 sm:p-6">
        <div className="flex items-start justify-between gap-4"><div><p className="eyebrow">Runtime Control Center</p><h3 className="mt-1 text-lg font-semibold text-stone-900">Model, memory and scheduling</h3></div><FileCheck2 className="text-orange-600" size={20}/></div>
        <div className="mt-6 space-y-4">
          <label className="block space-y-2"><span className="field-label">Absolute model path</span><input className="input" value={path} onChange={(e) => setPath(e.target.value)} placeholder="/Users/you/models/model.gguf" /></label>
          <div className="grid gap-4 sm:grid-cols-2"><label className="block space-y-2"><span className="field-label">Model alias</span><input className="input" value={modelID} onChange={(e) => setModelID(e.target.value)} /></label><label className="block space-y-2"><span className="field-label">Scheduler</span><select className="input" value={batching} onChange={(e) => setBatching(e.target.value as "queue" | "continuous")}><option value="queue">Queue · broad model path</option><option value="continuous">Continuous · qualified model only</option></select></label></div>
          <div className="grid gap-4 sm:grid-cols-2"><label className="block space-y-2"><span className="field-label">Context tokens</span><input className="input" type="number" min={32} max={1048576} value={contextTokens} onChange={(e) => setContextTokens(Number(e.target.value))}/></label><label className="block space-y-2"><span className="field-label">Maximum output tokens</span><input className="input" type="number" min={1} max={contextTokens} value={maxOutputTokens} onChange={(e) => setMaxOutputTokens(Number(e.target.value))}/></label></div>
          <div><span className="field-label">Memory presets</span><div className="mt-2 flex flex-wrap gap-2">{[["Interactive 4K",4096,512],["Balanced 8K",8192,1024],["Long context 32K",32768,2048]].map(([label,context,output]) => <button key={label} type="button" className="rounded-lg border border-orange-950/10 bg-white px-3 py-2 text-xs text-stone-700 hover:bg-orange-50" onClick={() => { setContextTokens(Number(context)); setMaxOutputTokens(Number(output)); }}>{label}</button>)}</div></div>
          <div className={`rounded-xl border p-4 ${memoryRatio != null && memoryRatio > .8 ? "border-amber-300 bg-amber-50" : "border-emerald-200 bg-emerald-50/60"}`}><div className="flex items-center justify-between gap-3"><div><p className="field-label">Estimated unified-memory plan</p><p className="mt-1 text-lg font-semibold text-stone-900">{estimatedTotal ? `${(estimatedTotal / 1024 ** 3).toFixed(2)} GiB` : "Unavailable"}</p></div>{memoryRatio != null && memoryRatio > .8 ? <AlertTriangle className="text-amber-700" size={20}/> : <ShieldCheck className="text-emerald-700" size={20}/>}</div><div className="mt-3 h-2 overflow-hidden rounded-full bg-white"><div className={`h-full ${memoryRatio != null && memoryRatio > .8 ? "bg-amber-500" : "bg-emerald-500"}`} style={{width:`${Math.min(100,(memoryRatio ?? 0)*100)}%`}}/></div><p className="mt-2 text-xs text-stone-500">Estimate = GGUF file size + context scaled from measured runtime allocation. {physical ? `Physical unified memory: ${(physical / 1024 ** 3).toFixed(0)} GiB.` : "Run the Driving Test to record physical memory."} Runtime overhead and macOS headroom remain additional; values are not reservations.</p></div>
          <div className="rounded-xl border border-orange-200 bg-orange-50/70 p-4 text-xs leading-relaxed text-stone-600"><Info size={15} className="mb-2 text-orange-700"/><strong>These are cold settings.</strong> Saving is harmless; applying drains admission and restarts the managed backend. Keep 4,096 / 512 unless you deliberately need more. Continuous mode divides the context between two active sequences.</div>
          {message ? <p className="rounded-xl border border-orange-950/10 bg-stone-50 p-3 text-xs text-stone-600">{message}</p> : null}
          <div className="flex flex-wrap gap-2"><Button variant="secondary" onClick={() => void save()} disabled={busy || !token || !path}><Save size={14}/>{busy ? "Working…" : "Save for next start"}</Button><Button onClick={() => void apply()} disabled={busy || !token || !path}><RotateCw size={14}/>Apply & restart backend</Button></div>
          {!token ? <p className="text-xs text-stone-500">Enter the local bearer token in the header to change private settings.</p> : null}
        </div>
      </Card>

      <div className="space-y-4">
        <Card className="p-5"><p className="eyebrow">Running now</p><div className="mt-4 flex items-center gap-3"><span className="grid size-10 place-items-center rounded-xl bg-emerald-50 text-emerald-700"><CheckCircle2 size={18}/></span><div><p className="text-sm font-semibold text-stone-900">{snapshot.models[0]?.id ?? "No ready model"}</p><p className="text-xs text-stone-500">{snapshot.backend?.state ?? "unavailable"}</p></div></div></Card>
        <Card className="p-5"><div className="flex items-start justify-between gap-3"><div><p className="eyebrow">Loaded-model capabilities</p><h3 className="mt-1 text-sm font-semibold text-stone-900">{capabilities ? capabilities.architecture : "Unavailable"}</h3></div>{capabilities ? <Badge className="border-emerald-500/20 bg-emerald-50 text-emerald-700">{capabilities.driving_test?.grade ?? capabilities.compatibility}</Badge> : null}</div>{capabilities ? <div className="mt-4 space-y-3 text-xs"><p className="text-stone-500">{(capabilities.parameter_count / 1e9).toFixed(2)}B parameters · {(capabilities.trained_context_tokens / 1024).toLocaleString(undefined, { maximumFractionDigits: 1 })}K declared context</p>{capabilities.driving_test ? <p className="rounded-lg bg-emerald-50 p-3 text-emerald-800">Driving Test: {Object.values(capabilities.driving_test.checks).filter(Boolean).length}/{Object.keys(capabilities.driving_test.checks).length} checks passed · continuous {capabilities.driving_test.continuous_qualified ? "qualified" : "not qualified"}</p> : null}<p className="font-medium text-stone-700">Running budget: {capabilities.configured_context_tokens.toLocaleString()} context · {capabilities.configured_max_output_tokens.toLocaleString()} output</p><div className="grid grid-cols-2 gap-2">{Object.entries(capabilities.capabilities).map(([name, support]) => <div key={name} className="rounded-lg border border-orange-950/[.07] bg-stone-50 px-3 py-2"><p className="text-[10px] uppercase tracking-wide text-stone-400">{name.replaceAll("_", " ")}</p><p className="mt-1 font-medium text-stone-700">{support}</p></div>)}</div><p className="leading-relaxed text-stone-400">Detected means the pinned template renderer accepted a bounded probe. Verified means PilotLlama has an exact tested path. Unqualified is intentionally not a compatibility claim.</p></div> : <p className="mt-4 text-xs leading-relaxed text-stone-500">Start and authenticate a compatible model to inspect its actual loaded GGUF metadata and template behavior.</p>}</Card>
        <Card className="p-5"><p className="eyebrow">Saved selection · cold settings</p>{saved ? <div className="mt-4 space-y-3 text-xs"><Row icon={HardDrive} label="File" value={saved.file_name}/><Row icon={ShieldCheck} label="Preflight" value="GGUF header only"/><Row icon={RefreshCw} label="Apply" value="Backend restart required"/><p className="break-all rounded-lg bg-stone-50 p-3 font-mono text-[10px] text-stone-500">{saved.path}</p><p className="text-stone-500">{(saved.size_bytes / 1024 ** 3).toFixed(2)} GiB · {saved.batching} · {saved.context_tokens.toLocaleString()} context · {saved.max_output_tokens.toLocaleString()} output</p></div> : <p className="mt-4 text-sm text-stone-500">Authenticate to read the saved local configuration.</p>}</Card>
      </div>
    </div>
  </div>;
}

function Row({ icon: Icon, label, value }: { icon: typeof HardDrive; label: string; value: string }) {
  return <div className="flex items-center gap-3"><Icon size={14} className="text-orange-600"/><div><p className="text-stone-400">{label}</p><p className="font-medium text-stone-700">{value}</p></div></div>;
}

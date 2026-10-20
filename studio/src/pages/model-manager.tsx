import { useEffect, useState } from "react";
import { CheckCircle2, FileCheck2, HardDrive, Info, RefreshCw, Save, ShieldCheck } from "lucide-react";
import type { ApiSnapshot } from "../lib/types";
import { saveModelConfig } from "../lib/api";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card } from "../components/ui/card";

export function ModelManager({ snapshot, token, refresh }: { snapshot: ApiSnapshot; token: string; refresh: () => Promise<void> }) {
  const saved = snapshot.modelConfig;
  const [path, setPath] = useState("");
  const [modelID, setModelID] = useState("local-model");
  const [batching, setBatching] = useState<"queue" | "continuous">("queue");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  useEffect(() => {
    if (!saved) return;
    setPath(saved.path); setModelID(saved.model_id); setBatching(saved.batching);
  }, [saved]);

  async function save() {
    setBusy(true); setMessage("");
    try {
      const result = await saveModelConfig(token, { path, model_id: modelID, batching });
      setMessage(`${result.file_name} passed GGUF container preflight and is saved for the next start.`);
      await refresh();
    } catch (error) { setMessage(error instanceof Error ? error.message : "Preflight failed"); }
    finally { setBusy(false); }
  }

  return <div className="space-y-5">
    <section className="hero-grid overflow-hidden rounded-2xl border border-orange-400/15 p-6 sm:p-8">
      <Badge className="border-orange-400/20 bg-orange-400/10 text-orange-700">Local models · no downloads</Badge>
      <h2 className="mt-5 text-3xl font-semibold tracking-[-.04em] text-stone-900">Choose what flies next.</h2>
      <p className="mt-3 max-w-2xl text-sm leading-relaxed text-stone-600">Point PilotLlama at an existing GGUF on this Mac. It checks the file safely and saves the next-launch configuration without loading a second model or interrupting the one running now.</p>
    </section>

    <div className="grid gap-4 xl:grid-cols-[1.25fr_.75fr]">
      <Card className="p-5 sm:p-6">
        <div className="flex items-start justify-between gap-4"><div><p className="eyebrow">Next launch</p><h3 className="mt-1 text-lg font-semibold text-stone-900">Local GGUF configuration</h3></div><FileCheck2 className="text-orange-600" size={20}/></div>
        <div className="mt-6 space-y-4">
          <label className="block space-y-2"><span className="field-label">Absolute model path</span><input className="input" value={path} onChange={(e) => setPath(e.target.value)} placeholder="/Users/you/models/model.gguf" /></label>
          <div className="grid gap-4 sm:grid-cols-2"><label className="block space-y-2"><span className="field-label">Model alias</span><input className="input" value={modelID} onChange={(e) => setModelID(e.target.value)} /></label><label className="block space-y-2"><span className="field-label">Scheduler</span><select className="input" value={batching} onChange={(e) => setBatching(e.target.value as "queue" | "continuous")}><option value="queue">Queue · broad model path</option><option value="continuous">Continuous · verified Nemotron only</option></select></label></div>
          <div className="rounded-xl border border-orange-200 bg-orange-50/70 p-4 text-xs leading-relaxed text-stone-600"><Info size={15} className="mb-2 text-orange-700"/><strong>Queue mode:</strong> supports decoder-only GGUF models that pinned llama.cpp recognizes and that carry a supported chat template. <strong>Continuous mode:</strong> remains restricted to the verified Nemotron shape.</div>
          {message ? <p className="rounded-xl border border-orange-950/10 bg-stone-50 p-3 text-xs text-stone-600">{message}</p> : null}
          <Button onClick={() => void save()} disabled={busy || !token || !path}><Save size={14}/>{busy ? "Checking…" : "Check and save"}</Button>
          {!token ? <p className="text-xs text-stone-500">Enter the local bearer token in the header to change private settings.</p> : null}
        </div>
      </Card>

      <div className="space-y-4">
        <Card className="p-5"><p className="eyebrow">Running now</p><div className="mt-4 flex items-center gap-3"><span className="grid size-10 place-items-center rounded-xl bg-emerald-50 text-emerald-700"><CheckCircle2 size={18}/></span><div><p className="text-sm font-semibold text-stone-900">{snapshot.models[0]?.id ?? "No ready model"}</p><p className="text-xs text-stone-500">{snapshot.backend?.state ?? "unavailable"}</p></div></div></Card>
        <Card className="p-5"><p className="eyebrow">Saved selection</p>{saved ? <div className="mt-4 space-y-3 text-xs"><Row icon={HardDrive} label="File" value={saved.file_name}/><Row icon={ShieldCheck} label="Preflight" value="GGUF header only"/><Row icon={RefreshCw} label="Apply" value="Stop, then start"/><p className="break-all rounded-lg bg-stone-50 p-3 font-mono text-[10px] text-stone-500">{saved.path}</p><p className="text-stone-500">{(saved.size_bytes / 1024 ** 3).toFixed(2)} GiB · {saved.batching}</p></div> : <p className="mt-4 text-sm text-stone-500">Authenticate to read the saved local configuration.</p>}</Card>
      </div>
    </div>
  </div>;
}

function Row({ icon: Icon, label, value }: { icon: typeof HardDrive; label: string; value: string }) {
  return <div className="flex items-center gap-3"><Icon size={14} className="text-orange-600"/><div><p className="text-stone-400">{label}</p><p className="font-medium text-stone-700">{value}</p></div></div>;
}

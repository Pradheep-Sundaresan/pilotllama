import { useState } from "react";
import { Activity, CircleDot, PauseCircle, PlayCircle, RefreshCw, Server, ShieldCheck } from "lucide-react";
import { control } from "../lib/api";
import type { ApiSnapshot } from "../lib/types";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card } from "../components/ui/card";

function StatusRow({ label, value, good }: { label: string; value: string; good?: boolean }) {
  return <div className="flex items-center justify-between gap-4 border-b border-orange-950/[.06] py-4 last:border-0"><span className="text-sm text-stone-500">{label}</span><span className={`flex items-center gap-2 text-sm font-medium ${good ? "text-emerald-700" : "text-stone-700"}`}><CircleDot size={13} />{value}</span></div>;
}

export function SystemHealth({ snapshot, token, refresh }: { snapshot: ApiSnapshot; token: string; refresh: () => Promise<void> }) {
  const [busy, setBusy] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  async function action(label: string, path: Parameters<typeof control>[0]) {
    setBusy(label); setMessage(null);
    try { await control(path, token); setMessage(`${label} completed.`); }
    catch (cause) { setMessage(cause instanceof Error ? cause.message : `${label} failed.`); }
    finally { setBusy(null); await refresh(); }
  }
  const backend = snapshot.backend;
  const admission = snapshot.admission ?? backend?.admission;

  return (
    <div className="space-y-4">
      <div className="grid gap-4 lg:grid-cols-3">
        <Card className="p-5"><div className="flex items-center gap-3"><span className="grid size-10 place-items-center rounded-xl bg-emerald-400/10 text-emerald-700"><Activity size={18} /></span><div><p className="field-label">HTTP service</p><p className="mt-1 text-lg font-semibold text-stone-900">{snapshot.health ? "Reachable" : "Offline"}</p></div></div></Card>
        <Card className="p-5"><div className="flex items-center gap-3"><span className="grid size-10 place-items-center rounded-xl bg-orange-400/10 text-orange-700"><Server size={18} /></span><div><p className="field-label">Inference readiness</p><p className="mt-1 text-lg font-semibold text-stone-900">{snapshot.ready?.ready ? "Ready" : "Unavailable"}</p></div></div></Card>
        <Card className="p-5"><div className="flex items-center gap-3"><span className="grid size-10 place-items-center rounded-xl bg-violet-400/10 text-violet-700"><ShieldCheck size={18} /></span><div><p className="field-label">Control access</p><p className="mt-1 text-lg font-semibold text-stone-900">{backend ? "Authenticated" : token ? "Unavailable" : "Token required"}</p></div></div></Card>
      </div>

      <div className="grid gap-4 xl:grid-cols-2">
        <Card className="p-5">
          <div className="flex items-start justify-between"><div><p className="eyebrow">Service state</p><h2 className="mt-1 text-lg font-semibold text-stone-900">Runtime status</h2></div><Button size="sm" variant="ghost" onClick={refresh}><RefreshCw size={14} /> Refresh</Button></div>
          <div className="mt-5"><StatusRow label="HTTP health" value={snapshot.health ? "handling requests" : "unreachable"} good={!!snapshot.health} /><StatusRow label="Backend readiness" value={snapshot.ready?.ready ? "ready" : snapshot.ready?.reason ?? "unavailable"} good={snapshot.ready?.ready} /><StatusRow label="Process manager" value={backend?.state ?? "unavailable"} good={backend?.state === "ready"} /><StatusRow label="Model alias" value={snapshot.models[0]?.id ?? "unavailable"} good={snapshot.models.length > 0} /><StatusRow label="Backend ownership" value={backend ? (backend.managed ? "managed child" : "external") : "unavailable"} /></div>
        </Card>

        <Card className="p-5">
          <div><p className="eyebrow">Controlled operations</p><h2 className="mt-1 text-lg font-semibold text-stone-900">Model lifecycle</h2><p className="mt-2 text-xs leading-relaxed text-stone-400">Controls close Go admission before changing the managed process. Starting preserves drain; resume explicitly when ready.</p></div>
          <div className="mt-6 grid grid-cols-2 gap-3"><Button variant="secondary" disabled={!token || !!busy || !backend?.managed} onClick={() => action("Start backend", "/internal/v1/backend/start")}><PlayCircle size={15} /> Start backend</Button><Button variant="danger" disabled={!token || !!busy || !backend?.managed} onClick={() => action("Stop backend", "/internal/v1/backend/stop")}><PauseCircle size={15} /> Stop backend</Button><Button variant="secondary" disabled={!token || !!busy} onClick={() => action("Drain admission", "/internal/v1/admission/drain")}><PauseCircle size={15} /> Drain</Button><Button disabled={!token || !!busy} onClick={() => action("Resume admission", "/internal/v1/admission/resume")}><PlayCircle size={15} /> Resume</Button></div>
          {message ? <p className="mt-4 rounded-lg border border-orange-950/[.06] bg-white/[.025] p-3 text-xs text-stone-600">{message}</p> : null}
          <div className="mt-6 rounded-xl border border-orange-950/[.06] bg-orange-950/[.035] p-4"><div className="flex items-center justify-between"><span className="text-sm text-stone-600">Generation admission</span><Badge className={admission?.state === "accepting" ? "border-emerald-400/20 bg-emerald-400/10 text-emerald-700" : "border-orange-400/20 bg-orange-400/10 text-orange-700"}>{admission?.state ?? "unavailable"}</Badge></div><div className="mt-4 flex justify-between text-xs text-stone-400"><span>Active Go handlers</span><span>{admission?.active_generation_handlers ?? "—"}</span></div><div className="mt-2 flex justify-between text-xs text-stone-400"><span>Native idle</span><span>Not observed</span></div></div>
        </Card>
      </div>
    </div>
  );
}

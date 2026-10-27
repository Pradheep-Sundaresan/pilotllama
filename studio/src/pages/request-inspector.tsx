import { useEffect, useState } from "react";
import { CheckCircle2, CircleHelp, CircleSlash2, Copy, Search, XCircle } from "lucide-react";
import type { ApiSnapshot, RequestRecord } from "../lib/types";
import { formatMetric, formatTime, shortID } from "../lib/utils";
import { EmptyState } from "../components/empty-state";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card } from "../components/ui/card";
import { Tooltip } from "../components/ui/tooltip";

function outcome(record: RequestRecord) {
  if (record.outcome === "completed") return <span className="flex items-center gap-1.5 text-emerald-700"><CheckCircle2 size={13} /> completed</span>;
  if (record.outcome === "delivery_failed") return <span className="flex items-center gap-1.5 text-violet-700"><CircleSlash2 size={13} /> delivery failed</span>;
  return <span className="flex items-center gap-1.5 text-red-700"><XCircle size={13} /> failed</span>;
}

function Value({ label, value, unit }: { label: string; value: string | number | null | undefined; unit?: string }) {
  const shown = typeof value === "number" ? formatMetric(value) : value ?? "Unavailable";
  const explanations: Record<string, string> = {
    "Handler latency": "Total time the Go API spent handling this request.",
    "RPC latency": "Time from Go calling the native engine until that call ended.",
    "RPC TTFT": "Time from Go calling the engine until the first token arrived.",
    "Receive rate": "How quickly Go received token events from the native engine.",
    "Token events": "How many generated token pieces crossed the internal stream.",
    "Token bytes": "The raw size of those generated pieces before browser display.",
    "Runtime": "Total time this request spent inside the native runtime.",
    "Queue wait": "Time spent waiting for a model execution slot.",
    "Prefill": "Time the model spent reading and understanding the prompt.",
    "Decode": "Time the model spent producing the answer after reading the prompt.",
    "Native TTFT": "Time from native submission until its first generated token.",
    "Decode rate": "How many tokens per second the native model generated.",
    "Drafted tokens": "Candidate tokens DFlash proposed for this request. Unavailable means DFlash was not measured.",
    "Accepted draft": "Proposed DFlash tokens that the target model accepted.",
    "Draft acceptance": "Accepted divided by drafted tokens. It measures useful speculation, not speedup by itself."
  };
  return <div className="rounded-xl border border-orange-950/[.06] bg-white p-3"><div className="flex items-center justify-between gap-2"><p className="text-[10px] uppercase tracking-[.12em] text-stone-400">{label}</p><Tooltip content={<><p className="font-semibold text-stone-900">In plain English</p><p className="mt-0.5">{explanations[label]}</p></>}><button className="rounded-full p-1 text-stone-400 hover:bg-orange-50 hover:text-orange-700" aria-label={`Explain ${label}`}><CircleHelp size={12} /></button></Tooltip></div><p className={`mt-1 text-sm font-medium ${shown === "Unavailable" ? "text-stone-400" : "text-stone-800"}`}>{shown}{unit && shown !== "Unavailable" ? ` ${unit}` : ""}</p></div>;
}

export function RequestInspector({ snapshot }: { snapshot: ApiSnapshot }) {
  const records = snapshot.history?.records ?? [];
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<RequestRecord | null>(records[0] ?? null);
  useEffect(() => {
    if (!selected && records[0]) setSelected(records[0]);
  }, [records, selected]);
  const shown = records.filter((record) => record.request_id.includes(query));

  return (
    <div className="grid min-h-[calc(100vh-9rem)] gap-4 xl:grid-cols-[minmax(470px,1.25fr)_minmax(360px,.75fr)]">
      <Card className="overflow-hidden">
        <div className="flex flex-wrap items-center justify-between gap-4 border-b border-orange-950/[.07] p-5"><div><p className="eyebrow">Bounded history</p><h2 className="mt-1 text-lg font-semibold text-stone-900">Request Inspector</h2></div><Badge>{records.length} / {snapshot.history?.capacity ?? 128} retained</Badge></div>
        <div className="border-b border-orange-950/[.07] p-3"><label className="relative block"><Search className="absolute left-3 top-1/2 -translate-y-1/2 text-stone-400" size={15} /><input className="input h-9 pl-9" placeholder="Filter by request ID" value={query} onChange={(event) => setQuery(event.target.value)} /></label></div>
        {shown.length ? <div className="overflow-x-auto"><table className="w-full text-left text-xs"><thead className="text-[10px] uppercase tracking-[.12em] text-stone-400"><tr className="border-b border-orange-950/[.06]"><th className="px-5 py-3 font-medium">Request</th><th className="px-4 py-3 font-medium">Outcome</th><th className="px-4 py-3 font-medium">Handler</th><th className="px-4 py-3 font-medium">TTFT</th><th className="px-4 py-3 font-medium">Tokens</th><th className="px-5 py-3 font-medium">Finished</th></tr></thead><tbody>{shown.map((record) => <tr key={record.request_id} onClick={() => setSelected(record)} className={`cursor-pointer border-b border-orange-950/[.05] transition hover:bg-orange-950/[.035] ${selected?.request_id === record.request_id ? "bg-orange-400/[.055]" : ""}`}><td className="px-5 py-4 font-mono text-stone-700">{shortID(record.request_id)}</td><td className="px-4 py-4">{outcome(record)}</td><td className="px-4 py-4 tabular-nums text-stone-600">{formatMetric(record.handler_latency_ms)} ms</td><td className="px-4 py-4 tabular-nums text-stone-600">{record.rpc_ttft_ms == null ? "—" : `${formatMetric(record.rpc_ttft_ms)} ms`}</td><td className="px-4 py-4 tabular-nums text-stone-600">{record.received_tokens}</td><td className="px-5 py-4 text-stone-500">{formatTime(record.finished_at_unix_ms)}</td></tr>)}</tbody></table></div> : <div className="p-5"><EmptyState title="No retained requests" detail="Only attempts that reach the gRPC Generate method are retained. Authentication and validation rejections do not appear here." /></div>}
      </Card>

      <Card className="overflow-hidden">
        {selected ? <><div className="border-b border-orange-950/[.07] p-5"><div className="flex items-center justify-between gap-3"><div><p className="eyebrow">Request detail</p><p className="mt-1 font-mono text-sm text-stone-800">{selected.request_id}</p></div><Button size="icon" variant="ghost" aria-label="Copy request ID" onClick={() => navigator.clipboard.writeText(selected.request_id)}><Copy size={14} /></Button></div><div className="mt-4">{outcome(selected)}</div></div><div className="space-y-6 p-5"><section><p className="field-label mb-3">Go / transport measurements</p><div className="grid grid-cols-2 gap-2"><Value label="Handler latency" value={selected.handler_latency_ms} unit="ms" /><Value label="RPC latency" value={selected.rpc_latency_ms} unit="ms" /><Value label="RPC TTFT" value={selected.rpc_ttft_ms} unit="ms" /><Value label="Receive rate" value={selected.receive_tokens_per_second} unit="tok/s" /><Value label="Token events" value={selected.received_tokens} /><Value label="Token bytes" value={selected.token_bytes} /></div></section><section><p className="field-label mb-3">Native measurements</p><div className="grid grid-cols-2 gap-2"><Value label="Runtime" value={selected.native?.runtime_latency_ms} unit="ms" /><Value label="Queue wait" value={selected.native?.queue_wait_ms} unit="ms" /><Value label="Prefill" value={selected.native?.prefill_phase_ms} unit="ms" /><Value label="Decode" value={selected.native?.decode_phase_ms} unit="ms" /><Value label="Native TTFT" value={selected.native?.ttft_ms} unit="ms" /><Value label="Decode rate" value={selected.native?.decode_tokens_per_second} unit="tok/s" /></div></section><section><p className="field-label mb-3">DFlash measurements</p><div className="grid grid-cols-2 gap-2"><Value label="Drafted tokens" value={selected.native?.speculative_drafted_tokens} /><Value label="Accepted draft" value={selected.native?.speculative_accepted_tokens} /><Value label="Draft acceptance" value={selected.native?.speculative_acceptance_rate == null ? null : selected.native.speculative_acceptance_rate * 100} unit="%" /></div></section><p className="text-[11px] leading-relaxed text-stone-400">Metrics preserve server nulls. RPC TTFT begins when Go opens the RPC; native TTFT begins at runtime submission and includes native queueing. Neither measures browser-visible TTFT.</p></div></> : <div className="grid h-full min-h-72 place-items-center text-sm text-stone-400">Select a retained request.</div>}
      </Card>
    </div>
  );
}

import { Activity, Boxes, Cpu, Database, Download, Gauge, HardDrive, Info, Layers3, Thermometer } from "lucide-react";
import { Bar, BarChart, CartesianGrid, Legend, ResponsiveContainer, Tooltip as ChartTooltip, XAxis, YAxis } from "recharts";
import { phase5cCsv, phase5cReport } from "../data/phase5c";
import type { ApiSnapshot } from "../lib/types";
import { formatMetric, formatTime } from "../lib/utils";
import { MetricCard } from "../components/metric-card";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardHeader } from "../components/ui/card";

const tooltipStyle = { background: "#ffffff", border: "1px solid rgba(91,54,34,.12)", borderRadius: 10, color: "#3f3029" };
const pointLabels: Record<string, string> = {
  backend_ready: "Backend ready", request_start: "Request start", prefill_complete: "Prefill complete",
  request_complete_before_cleanup: "Before cleanup", request_complete_after_cleanup: "After cleanup"
};

function mib(value: number | null | undefined) {
  return value == null ? "Unavailable" : formatMetric(value / 1048576, 1);
}

function downloadText(filename: string, text: string, type: string) {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const anchor = document.createElement("a");
  anchor.href = url; anchor.download = filename; anchor.click();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}

export function MemoryLab({ snapshot }: { snapshot: ApiSnapshot }) {
  const memory = snapshot.memory;
  const latest = memory?.process_samples.at(-1);
  const liveChart = memory?.process_samples.map((sample) => ({
    point: pointLabels[sample.point] ?? sample.point,
    rss: sample.resident_bytes == null ? null : sample.resident_bytes / 1048576,
    footprint: sample.physical_footprint_bytes == null ? null : sample.physical_footprint_bytes / 1048576
  })) ?? [];
  const recordedChart = phase5cReport.observations.map((item) => ({
    stage: item.stage, rss: item.resident / 1048576, footprint: item.footprint / 1048576
  }));

  return <div className="space-y-5">
    <section className="hero-grid overflow-hidden rounded-2xl border border-orange-400/15 px-6 py-7 sm:px-8">
      <div className="relative z-10 flex flex-wrap items-end justify-between gap-6">
        <div className="max-w-2xl"><div className="flex flex-wrap gap-2"><Badge className="border-orange-400/20 bg-orange-400/10 text-orange-700">Phase 5 · measured</Badge><Badge>Apple unified memory</Badge></div><h2 className="mt-5 text-3xl font-semibold tracking-[-.04em] text-stone-900 sm:text-4xl">See what the model keeps in memory.</h2><p className="mt-3 max-w-xl text-sm leading-relaxed text-stone-600">Live process samples and llama.cpp allocation accounting, with a reproducible native baseline. RSS and physical footprint use different macOS accounting and must not be added together.</p></div>
        <div className={`flex items-center gap-2 rounded-full border px-3 py-1.5 text-xs ${memory ? "border-emerald-400/20 bg-emerald-400/10 text-emerald-700" : "border-orange-950/10 bg-white/70 text-stone-500"}`}><span className={`size-1.5 rounded-full ${memory ? "bg-emerald-400" : "bg-zinc-600"}`} />{memory ? "Live snapshot" : "Live unavailable"}</div>
      </div>
    </section>

    <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <MetricCard icon={HardDrive} label="Current RSS" value={mib(latest?.resident_bytes)} unit="MiB" unavailable={!latest?.resident_bytes} simple="Memory pages currently sitting in physical memory for the native model process." detail="Latest measured resident bytes from macOS task_info for the native backend process." />
      <MetricCard icon={Gauge} label="Peak RSS" value={mib(latest?.peak_resident_bytes)} unit="MiB" unavailable={!latest?.peak_resident_bytes} simple="The highest resident-memory reading seen since this model process started." detail="Lifetime peak resident bytes reported by macOS at the latest lifecycle sample." />
      <MetricCard icon={Activity} label="Physical footprint" value={mib(latest?.physical_footprint_bytes)} unit="MiB" unavailable={!latest?.physical_footprint_bytes} simple="macOS’s estimate of how much real memory pressure this process creates. Do not add it to RSS." detail="Latest macOS physical-footprint measurement. Its accounting differs from RSS; do not add them." />
      <MetricCard icon={Cpu} label="Status RPC" value={formatMetric(memory?.status_rpc_latency_ms, 2)} unit="ms" unavailable={!memory} simple="How long the dashboard’s backend took to retrieve this memory report." detail="Go monotonic elapsed time for the localhost status RPC carrying this memory snapshot." />
    </div>

    <div className="grid gap-4 xl:grid-cols-[1.25fr_.75fr]">
      <Card><CardHeader><div><p className="eyebrow">Live lifecycle snapshot</p><h3 className="mt-1 text-base font-semibold text-stone-900">Process accounting by stage</h3></div><Badge>MiB</Badge></CardHeader><CardContent className="h-80">
        {liveChart.length ? <ResponsiveContainer width="100%" height="100%"><BarChart data={liveChart} margin={{ top: 12, right: 8, left: 0, bottom: 28 }}><CartesianGrid stroke="rgba(91,54,34,.09)" vertical={false} /><XAxis dataKey="point" angle={-18} textAnchor="end" tick={{ fill: "#8c7466", fontSize: 10 }} tickLine={false} axisLine={false} /><YAxis tick={{ fill: "#8c7466", fontSize: 10 }} tickLine={false} axisLine={false} /><ChartTooltip contentStyle={tooltipStyle} /><Legend wrapperStyle={{ fontSize: 11 }} /><Bar dataKey="rss" name="RSS" fill="#fb923c" radius={[4,4,0,0]} /><Bar dataKey="footprint" name="Physical footprint" fill="#a78bfa" radius={[4,4,0,0]} /></BarChart></ResponsiveContainer> : <div className="grid h-full place-items-center text-center"><div><Database className="mx-auto text-stone-400" /><p className="mt-3 text-sm text-stone-500">Start an instrumented native backend and provide the API token.</p><p className="mt-1 text-xs text-stone-400">The recorded baseline below remains available offline.</p></div></div>}
      </CardContent></Card>

      <Card className="p-5 sm:p-6"><p className="eyebrow">llama.cpp allocations</p><h3 className="mt-1 text-base font-semibold text-stone-900">Logical buffer ownership</h3><div className="mt-5 space-y-3"><Allocation icon={Database} label="Model" value={mib(memory?.llama_model_allocated_bytes)} /><Allocation icon={Layers3} label="Hybrid context" value={mib(memory?.llama_context_allocated_bytes)} /><Allocation icon={Boxes} label="Compute" value={mib(memory?.llama_compute_allocated_bytes)} /></div><div className="mt-5 rounded-xl border border-orange-400/10 bg-orange-400/[.04] p-4 text-[11px] leading-relaxed text-stone-500"><Info size={14} className="mb-2 text-orange-600" />Allocation rows overlap process accounting. They describe llama buffers across CPU and Metal; they are not separate RAM and VRAM totals.</div>{memory ? <p className="mt-4 text-[10px] text-stone-400">ctx {memory.configuration.context_tokens} · batch {memory.configuration.batch_tokens} · seq {memory.configuration.sequences} · GPU layers {memory.configuration.gpu_layers}</p> : null}</Card>
    </div>

    <Card><CardHeader className="flex-wrap"><div><p className="eyebrow">Recorded Phase 5C baseline</p><h3 className="mt-1 text-base font-semibold text-stone-900">Two sequential native requests</h3></div><div className="flex gap-2"><Button variant="secondary" size="sm" onClick={() => downloadText("inferno-phase-5c.json", JSON.stringify(phase5cReport, null, 2) + "\n", "application/json")}><Download size={13} />JSON</Button><Button variant="secondary" size="sm" onClick={() => downloadText("inferno-phase-5c.csv", phase5cCsv, "text/csv")}><Download size={13} />CSV</Button></div></CardHeader><CardContent>
      <div className="grid gap-6 xl:grid-cols-[1fr_260px]"><div className="h-72"><ResponsiveContainer width="100%" height="100%"><BarChart data={recordedChart} margin={{ top: 12, right: 8, left: 0, bottom: 18 }}><CartesianGrid stroke="rgba(91,54,34,.09)" vertical={false} /><XAxis dataKey="stage" tick={{ fill: "#8c7466", fontSize: 10 }} tickLine={false} axisLine={false} /><YAxis tick={{ fill: "#8c7466", fontSize: 10 }} tickLine={false} axisLine={false} unit=" MiB" /><ChartTooltip contentStyle={tooltipStyle} /><Legend wrapperStyle={{ fontSize: 11 }} /><Bar dataKey="rss" name="RSS" fill="#fb923c" radius={[4,4,0,0]} /><Bar dataKey="footprint" name="Physical footprint" fill="#a78bfa" radius={[4,4,0,0]} /></BarChart></ResponsiveContainer></div><div className="space-y-2"><Fact icon={Database} label="Model allocation" value={`${mib(phase5cReport.allocation.model)} MiB`} /><Fact icon={Layers3} label="Context allocation" value={`${mib(phase5cReport.allocation.context)} MiB`} /><Fact icon={Thermometer} label="Thermal reading" value="Unavailable" /></div></div>
      <div className="mt-4 border-t border-orange-950/[.06] pt-4 text-[11px] leading-relaxed text-stone-400">Real cached Nemotron 4B Metal run captured {new Date(phase5cReport.capturedAt).toLocaleString()}: two sequential one-token requests, both returned “4”. This is not a stress test, concurrency result, leak proof, or evidence that two active sequences are safe. Post-run system memory reported {phase5cReport.postRunMemoryFreePercent}% free; thermal status was unavailable. <span className="font-mono">{phase5cReport.resultPath}</span></div>
    </CardContent></Card>

    <p className="text-[11px] text-stone-400">Live source: {memory?.source ?? "unavailable"} · observed {memory ? formatTime(memory.observed_at_unix_ms) : "—"}. Lifecycle values are the latest sample per stage, not a time series.</p>
  </div>;
}

function Allocation({ icon: Icon, label, value }: { icon: typeof Database; label: string; value: string }) { return <div className="flex items-center justify-between rounded-xl border border-orange-950/[.06] bg-white/[.025] p-3"><span className="flex items-center gap-3 text-xs text-stone-500"><Icon size={15} />{label}</span><span className="font-mono text-sm text-stone-800">{value} {value !== "Unavailable" ? "MiB" : ""}</span></div>; }
function Fact({ icon: Icon, label, value }: { icon: typeof Database; label: string; value: string }) { return <div className="rounded-xl border border-orange-950/[.06] bg-white/[.025] p-3"><Icon size={14} className="text-stone-400" /><p className="mt-3 text-[10px] uppercase tracking-[.13em] text-stone-400">{label}</p><p className="mt-1 text-sm font-semibold text-stone-700">{value}</p></div>; }

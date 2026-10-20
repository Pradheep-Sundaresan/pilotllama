import { useState } from "react";
import { Activity, CheckCircle2, Clock3, Download, Gauge, HardDrive, Layers3, TimerReset, Users, Zap } from "lucide-react";
import { Bar, BarChart, CartesianGrid, Legend, Line, LineChart, ResponsiveContainer, Tooltip as ChartTooltip, XAxis, YAxis } from "recharts";
import { phase6cCsv, phase6cReport } from "../data/phase6c";
import { formatMetric } from "../lib/utils";
import { MetricCard } from "../components/metric-card";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardHeader } from "../components/ui/card";

const tooltipStyle = { background: "#ffffff", border: "1px solid rgba(91,54,34,.12)", borderRadius: 10, color: "#3f3029" };
const formatBytes = (value: number) => `${formatMetric(value / 1048576, 1)} MiB`;
function downloadText(filename: string, text: string, type: string) {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const anchor = document.createElement("a"); anchor.href = url; anchor.download = filename; anchor.click();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}

export function BenchmarkLab() {
  const [traceMode, setTraceMode] = useState<"queue" | "continuous">("continuous");
  const queue = phase6cReport.cases.find((item) => item.mode === "Queue" && item.concurrency === 2)!;
  const continuous = phase6cReport.cases.find((item) => item.mode === "Continuous" && item.concurrency === 2)!;
  const comparison = [queue, continuous].map((item) => ({ mode: item.mode, ttft: item.ttftMs, queueWait: item.queueWaitMs, latency: item.latencyMs }));
  const throughput = [queue, continuous].map((item) => ({ mode: item.mode, throughput: item.throughput }));
  const ttftChange = (1 - continuous.ttftMs / queue.ttftMs) * 100;
  const throughputChange = (continuous.throughput / queue.throughput - 1) * 100;

  return <div className="space-y-5">
    <section className="hero-grid overflow-hidden rounded-2xl border border-orange-400/15 px-6 py-7 sm:px-8">
      <div className="relative z-10 flex flex-wrap items-end justify-between gap-6">
        <div className="max-w-2xl"><div className="flex flex-wrap gap-2"><Badge className="border-orange-400/20 bg-orange-400/10 text-orange-700">Phase 6C · measured A/B</Badge><Badge>Apple M4 · Q4_K_M</Badge></div><h2 className="mt-5 text-3xl font-semibold tracking-[-.04em] text-stone-900 sm:text-4xl">Two users start together.</h2><p className="mt-3 max-w-xl text-sm leading-relaxed text-stone-600">Queue mode maximizes one stream at a time. Continuous mode shares progress across two live streams: a faster response start with a small throughput cost.</p></div>
        <div className="flex gap-2"><Button variant="secondary" size="sm" onClick={() => downloadText("inferno-phase-6c.json", JSON.stringify(phase6cReport, null, 2) + "\n", "application/json")}><Download size={13}/>JSON</Button><Button variant="secondary" size="sm" onClick={() => downloadText("inferno-phase-6c.csv", phase6cCsv, "text/csv")}><Download size={13}/>CSV</Button></div>
      </div>
    </section>

    <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <MetricCard icon={TimerReset} label="TTFT improvement" value={formatMetric(ttftChange, 1)} unit="%" simple="With two users, continuous batching made the typical first word arrive much sooner." detail="Two-client P50 fell from 1,146.7 ms to 262.3 ms. Client POST start to first non-empty SSE content." />
      <MetricCard icon={Users} label="Active together" value="2" unit="requests" simple="The model visibly advanced two users’ responses at the same time." detail="Maximum C++ executing count observed in 82 scheduler snapshots during continuous mode." />
      <MetricCard icon={Zap} label="Throughput change" value={formatMetric(throughputChange, 1)} unit="%" simple="Total text production became slightly slower while both users received earlier progress." detail="22.04 tok/s continuous versus 23.07 tok/s queue. This is a measured degradation." />
      <MetricCard icon={CheckCircle2} label="Correct completions" value="12/12" simple="Every request in this small experiment finished and produced the expected output." detail="Every measured request completed. Delivered outputs shared the same SHA-256 digest." />
    </div>

    <div className="grid gap-4 xl:grid-cols-[1.35fr_1fr]">
      <Card><CardHeader><div><p className="eyebrow">Two-client experience</p><h3 className="mt-1 text-base font-semibold text-stone-900">Start sooner, finish slightly later</h3></div><Badge>P50 · ms</Badge></CardHeader><CardContent className="h-80"><ResponsiveContainer width="100%" height="100%"><BarChart data={comparison} margin={{ top: 12, right: 8, left: -10 }}><CartesianGrid stroke="rgba(91,54,34,.09)" vertical={false}/><XAxis dataKey="mode" tick={{ fill: "#8c7466", fontSize: 11 }} tickLine={false} axisLine={false}/><YAxis tick={{ fill: "#8c7466", fontSize: 10 }} tickLine={false} axisLine={false} unit="ms"/><ChartTooltip contentStyle={tooltipStyle}/><Legend wrapperStyle={{ fontSize: 11 }}/><Bar dataKey="ttft" name="TTFT" fill="#fb923c" radius={[4,4,0,0]}/><Bar dataKey="queueWait" name="Queue wait" fill="#a78bfa" radius={[4,4,0,0]}/><Bar dataKey="latency" name="Completion latency" fill="#facc15" radius={[4,4,0,0]}/></BarChart></ResponsiveContainer></CardContent></Card>
      <Card><CardHeader><div><p className="eyebrow">Aggregate capacity</p><h3 className="mt-1 text-base font-semibold text-stone-900">No throughput win yet</h3></div><Badge>tokens/s</Badge></CardHeader><CardContent className="h-80"><ResponsiveContainer width="100%" height="100%"><BarChart data={throughput} margin={{ top: 16, right: 16, left: -6 }}><CartesianGrid stroke="rgba(91,54,34,.09)" vertical={false}/><XAxis dataKey="mode" tick={{ fill: "#8c7466", fontSize: 11 }} tickLine={false} axisLine={false}/><YAxis domain={[20,24]} tick={{ fill: "#8c7466", fontSize: 10 }} tickLine={false} axisLine={false}/><ChartTooltip contentStyle={tooltipStyle}/><Bar dataKey="throughput" name="Throughput" fill="#34d399" radius={[5,5,0,0]}/></BarChart></ResponsiveContainer></CardContent><div className="border-t border-orange-950/[.06] px-5 py-4 text-xs leading-relaxed text-stone-500">Continuous batching traded 4.5% aggregate throughput for a 77.1% lower median TTFT. It improved fairness and responsiveness on this workload.</div></Card>
    </div>

    <Card><CardHeader className="flex-wrap"><div><p className="eyebrow">Measured scheduler occupancy</p><h3 className="mt-1 text-base font-semibold text-stone-900">One worker or two active sequences</h3></div><div className="flex rounded-lg border border-orange-950/[.07] bg-orange-950/[.04] p-1">{(["queue","continuous"] as const).map((mode) => <button key={mode} onClick={() => setTraceMode(mode)} className={`rounded-md px-3 py-1.5 text-xs capitalize transition ${traceMode === mode ? "bg-orange-400/15 text-orange-700" : "text-stone-400 hover:text-stone-700"}`}>{mode}</button>)}</div></CardHeader><CardContent><div className="grid gap-5 xl:grid-cols-[1fr_230px]"><div className="h-72"><ResponsiveContainer width="100%" height="100%"><LineChart data={phase6cReport.transitions[traceMode]} margin={{ top: 12, right: 12, left: -20 }}><CartesianGrid stroke="rgba(91,54,34,.09)" vertical={false}/><XAxis dataKey="offsetMs" type="number" tick={{ fill: "#8c7466", fontSize: 10 }} tickLine={false} axisLine={false} unit="ms"/><YAxis domain={[0,2]} ticks={[0,1,2]} allowDecimals={false} tick={{ fill: "#8c7466", fontSize: 10 }} tickLine={false} axisLine={false}/><ChartTooltip contentStyle={tooltipStyle}/><Legend wrapperStyle={{ fontSize: 11 }}/><Line type="stepAfter" dataKey="waiting" name="Waiting" stroke="#fb923c" strokeWidth={2.5} dot={false}/><Line type="stepAfter" dataKey="executing" name="Executing" stroke="#34d399" strokeWidth={2.5} dot={false}/></LineChart></ResponsiveContainer></div><div className="grid content-start gap-2 sm:grid-cols-3 xl:grid-cols-1"><Fact icon={Activity} label="Execution" value={traceMode === "continuous" ? "2 together" : "1 at a time"}/><Fact icon={Layers3} label="Max waiting" value={traceMode === "continuous" ? "0" : "1"}/><Fact icon={Gauge} label="Samples" value={traceMode === "continuous" ? "82" : "78"}/></div></div><p className="mt-3 text-[11px] leading-relaxed text-stone-400">Compact transitions from real C++ snapshots sampled every 50 ms during the two-client cases. This is sampled occupancy, not a continuous trace.</p></CardContent></Card>

    <div className="grid gap-4 xl:grid-cols-2">
      <Card className="p-5 sm:p-6"><p className="eyebrow">Measured memory tradeoff</p><h3 className="mt-1 text-base font-semibold text-stone-900">A second sequence needs more context state</h3><div className="mt-5 grid gap-3 sm:grid-cols-2"><MemoryMode title="Queue" rss={phase6cReport.memory.queue.peakRssBytes} footprint={phase6cReport.memory.queue.peakFootprintBytes} context={phase6cReport.memory.queue.contextBytes}/><MemoryMode title="Continuous" rss={phase6cReport.memory.continuous.peakRssBytes} footprint={phase6cReport.memory.continuous.peakFootprintBytes} context={phase6cReport.memory.continuous.contextBytes}/></div></Card>
      <Card className="p-5 sm:p-6"><p className="eyebrow">Evidence boundaries</p><h3 className="mt-1 text-base font-semibold text-stone-900">Small, controlled, honest</h3><div className="mt-5 grid gap-3 sm:grid-cols-3"><Missing label="P95 / P99" reason="Too few samples"/><Missing label="Thermal" reason="Unavailable"/><Missing label="Speedup" reason="Not observed" icon={Zap}/></div><div className="mt-5 rounded-xl border border-orange-950/[.06] bg-orange-950/[.04] p-4 text-[11px] leading-relaxed text-stone-500"><p>{phase6cReport.hardware} · {phase6cReport.model}</p><p className="mt-1">One excluded warm-up per mode · max {phase6cReport.maxOutputTokens} tokens · memory free {phase6cReport.systemMemoryFree.beforePercent}% → {phase6cReport.systemMemoryFree.afterPercent}%</p><p className="mt-3 break-all font-mono text-stone-400">{phase6cReport.resultPath}</p></div></Card>
    </div>
  </div>;
}

function Fact({ icon: Icon, label, value }: { icon: typeof Activity; label: string; value: string }) { return <div className="rounded-xl border border-orange-950/[.06] bg-white/[.025] p-3"><Icon size={14} className="text-stone-400"/><p className="mt-3 text-[10px] uppercase tracking-[.13em] text-stone-400">{label}</p><p className="mt-1 text-lg font-semibold text-stone-800">{value}</p></div>; }
function MemoryMode({ title, rss, footprint, context }: { title: string; rss: number; footprint: number; context: number }) { return <div className="rounded-xl border border-orange-950/[.06] bg-white/70 p-4"><div className="flex items-center gap-2"><HardDrive size={14} className="text-orange-600"/><p className="text-sm font-semibold text-stone-800">{title}</p></div><dl className="mt-4 space-y-2 text-xs"><div className="flex justify-between"><dt className="text-stone-400">Peak RSS</dt><dd className="text-stone-700">{formatBytes(rss)}</dd></div><div className="flex justify-between"><dt className="text-stone-400">Peak footprint</dt><dd className="text-stone-700">{formatBytes(footprint)}</dd></div><div className="flex justify-between"><dt className="text-stone-400">Context allocation</dt><dd className="text-stone-700">{formatBytes(context)}</dd></div></dl></div>; }
function Missing({ label, reason, icon: Icon = Clock3 }: { label: string; reason: string; icon?: typeof Clock3 }) { return <div className="rounded-xl border border-dashed border-orange-950/[.08] bg-white/[.015] p-3"><Icon size={14} className="text-stone-400"/><p className="mt-4 text-sm font-semibold text-stone-500">{label}</p><p className="mt-1 text-[10px] text-stone-400">{reason}</p></div>; }

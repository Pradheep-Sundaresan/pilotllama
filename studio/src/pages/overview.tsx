import { Activity, Clock3, Database, Gauge, MemoryStick, TimerReset, Zap } from "lucide-react";
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip as ChartTooltip,
  XAxis,
  YAxis
} from "recharts";
import type { ApiSnapshot } from "../lib/types";
import { formatMetric, formatTime } from "../lib/utils";
import { EmptyState } from "../components/empty-state";
import { MetricCard } from "../components/metric-card";
import { Badge } from "../components/ui/badge";
import { Card, CardContent, CardHeader } from "../components/ui/card";

const tooltipStyle = { background: "#ffffff", border: "1px solid rgba(91,54,34,.12)", borderRadius: 10, color: "#3f3029" };

export function Overview({ snapshot }: { snapshot: ApiSnapshot }) {
  const m = snapshot.metrics;
  const records = snapshot.history?.records ?? [];
  const latency = m
    ? [
        { name: "P50", handler: m.handler_latency_ms.p50, rpc: m.rpc_latency_ms.p50, ttft: m.rpc_ttft_ms.p50 },
        { name: "P95", handler: m.handler_latency_ms.p95, rpc: m.rpc_latency_ms.p95, ttft: m.rpc_ttft_ms.p95 },
        { name: "P99", handler: m.handler_latency_ms.p99, rpc: m.rpc_latency_ms.p99, ttft: m.rpc_ttft_ms.p99 }
      ]
    : [];
  const outcomes = m
    ? [
        { name: "Completed", value: m.completed, color: "#fb923c" },
        { name: "Failed", value: m.failed, color: "#f43f5e" },
        { name: "Delivery", value: m.delivery_failed, color: "#a78bfa" }
      ].filter((item) => item.value > 0)
    : [];
  const trend = [...records]
    .reverse()
    .slice(-24)
    .map((record) => ({ time: formatTime(record.finished_at_unix_ms), latency: record.handler_latency_ms, ttft: record.rpc_ttft_ms }));

  return (
    <div className="space-y-5">
      <section className="hero-grid overflow-hidden rounded-2xl border border-orange-400/15">
        <div className="relative z-10 grid items-center gap-5 px-6 py-7 sm:px-8 lg:min-h-[310px] lg:grid-cols-[minmax(0,1fr)_340px] lg:gap-8 lg:py-8">
          <div className="max-w-2xl">
            <Badge className="border-orange-400/20 bg-orange-400/10 text-orange-700">Live · 60 second window</Badge>
            <p className="mt-5 text-[11px] font-semibold uppercase tracking-[.2em] text-orange-700">Your Mac. Your models. Your AI service.</p>
            <h2 className="mt-2 text-3xl font-semibold tracking-[-.04em] text-stone-900 sm:text-4xl">Turn Apple Silicon into a private inference server.</h2>
            <p className="mt-3 max-w-xl text-sm leading-relaxed text-stone-600">
              PilotLlama runs supported local GGUF models on your Mac, exposes an OpenAI-style API for your apps, handles concurrent users with bounded scheduling, and shows exactly how requests, speed, and memory behave—without sending prompts to a cloud model.
            </p>
          </div>
          <div className="pilot-portrait relative mx-auto h-64 w-full max-w-[340px] overflow-hidden rounded-[2rem] border border-white/70 shadow-[0_22px_55px_rgba(123,62,30,.16)] lg:h-[270px]">
            <img src="/pilotllama-hero.png" alt="PilotLlama flying an orange propeller plane above the clouds" className="h-full w-full object-cover object-[center_39%]" />
            <div className="absolute inset-x-0 bottom-0 bg-gradient-to-t from-[#6f321d]/55 to-transparent px-5 pb-4 pt-12 text-right text-[10px] font-semibold uppercase tracking-[.16em] text-white">Local AI · ready to fly</div>
          </div>
        </div>
      </section>

      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <MetricCard icon={TimerReset} label="RPC TTFT · P50" value={formatMetric(m?.rpc_ttft_ms.p50)} unit="ms" simple="How long a typical request waits before its first word starts arriving. Lower feels faster." detail={`Go RPC open to first validated token event. ${m?.rpc_ttft_ms.samples ?? 0} completed samples in the current window.`} unavailable={m?.rpc_ttft_ms.p50 == null} />
        <MetricCard icon={Gauge} label="Native decode · P50" value={formatMetric(m?.native_decode_tokens_per_second.p50)} unit="tok/s" simple="How quickly the model writes once it has started. More tokens per second means faster text generation." detail={`Native C++ decode rate, excluding EOG. ${m?.native_decode_tokens_per_second.samples ?? 0} completed samples.`} unavailable={m?.native_decode_tokens_per_second.p50 == null} />
        <MetricCard icon={Zap} label="Aggregate throughput" value={formatMetric(m?.completed_native_tokens_per_second, 2)} unit="tok/s" simple="How much finished text the whole service produces over time, including moments when it is idle." detail="Completed native output tokens divided by the actual observation-window duration, including idle time. Completion-attributed, not instantaneous throughput." unavailable={m?.completed_native_tokens_per_second == null} />
        <MetricCard icon={Activity} label="Requests observed" value={m ? String(m.samples) : "Unavailable"} simple="How many finished model requests are represented in the current dashboard window." detail="Finalized Generate attempts retained within the current rolling window. Pre-RPC rejections and in-flight requests are excluded." unavailable={!m} />
      </div>

      <div className="grid gap-4 xl:grid-cols-[1.4fr_1fr]">
        <Card>
          <CardHeader>
            <div><p className="eyebrow">Request signal</p><h3 className="mt-1 text-base font-semibold text-stone-900">Recent handler latency</h3></div>
            <Badge>{trend.length} retained points</Badge>
          </CardHeader>
          <CardContent className="h-72">
            {trend.length ? (
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={trend} margin={{ top: 12, right: 8, left: -20, bottom: 0 }}>
                  <defs><linearGradient id="latencyFill" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stopColor="#f97316" stopOpacity={0.35} /><stop offset="100%" stopColor="#f97316" stopOpacity={0} /></linearGradient></defs>
                  <CartesianGrid stroke="rgba(91,54,34,.09)" vertical={false} />
                  <XAxis dataKey="time" tick={{ fill: "#8c7466", fontSize: 10 }} tickLine={false} axisLine={false} minTickGap={36} />
                  <YAxis tick={{ fill: "#8c7466", fontSize: 10 }} tickLine={false} axisLine={false} unit="ms" />
                  <ChartTooltip contentStyle={tooltipStyle} />
                  <Area type="monotone" dataKey="latency" name="Handler latency (ms)" stroke="#fb923c" fill="url(#latencyFill)" strokeWidth={2} />
                </AreaChart>
              </ResponsiveContainer>
            ) : <EmptyState title="No completed request observations" detail="Run a prompt in Playground. The chart will use its measured handler latency." />}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <div><p className="eyebrow">Outcomes</p><h3 className="mt-1 text-base font-semibold text-stone-900">Window composition</h3></div>
            <Badge>{m?.samples ?? 0} samples</Badge>
          </CardHeader>
          <CardContent className="h-72">
            {outcomes.length ? (
              <ResponsiveContainer width="100%" height="100%">
                <PieChart><Pie data={outcomes} dataKey="value" nameKey="name" innerRadius="58%" outerRadius="82%" paddingAngle={4} stroke="none">{outcomes.map((item) => <Cell key={item.name} fill={item.color} />)}</Pie><ChartTooltip contentStyle={tooltipStyle} /></PieChart>
              </ResponsiveContainer>
            ) : <EmptyState title="No outcomes in this window" detail="The rolling report only includes retained requests finalized during the current window." />}
          </CardContent>
        </Card>
      </div>

      <div className="grid gap-4 xl:grid-cols-[1.4fr_1fr]">
        <Card>
          <CardHeader>
            <div><p className="eyebrow">Empirical nearest rank</p><h3 className="mt-1 text-base font-semibold text-stone-900">Latency distribution</h3></div>
            <Badge>P95 needs 20 · P99 needs 100</Badge>
          </CardHeader>
          <CardContent className="h-72">
            {m?.handler_latency_ms.p50 != null ? (
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={latency} margin={{ top: 12, right: 8, left: -20, bottom: 0 }}>
                  <CartesianGrid stroke="rgba(91,54,34,.09)" vertical={false} />
                  <XAxis dataKey="name" tick={{ fill: "#8c7466", fontSize: 11 }} tickLine={false} axisLine={false} />
                  <YAxis tick={{ fill: "#8c7466", fontSize: 10 }} tickLine={false} axisLine={false} unit="ms" />
                  <ChartTooltip contentStyle={tooltipStyle} />
                  <Bar dataKey="handler" name="Handler (ms)" fill="#fb923c" radius={[4, 4, 0, 0]} />
                  <Bar dataKey="rpc" name="RPC (ms)" fill="#facc15" radius={[4, 4, 0, 0]} />
                  <Bar dataKey="ttft" name="RPC TTFT (ms)" fill="#a78bfa" radius={[4, 4, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            ) : <EmptyState title="No latency distribution yet" detail="P50 appears after one completed measured request. Tail percentiles remain unavailable until their sample thresholds are met." />}
          </CardContent>
        </Card>

        <Card className="p-5">
          <p className="eyebrow">Instrumentation coverage</p>
          <h3 className="mt-1 text-base font-semibold text-stone-900">What the runtime can see</h3>
          <div className="mt-6 space-y-4">
            {[
              [Clock3, "Request lifecycle", m ? `${m.retained_records} / ${m.history_capacity} retained` : "Unavailable"],
              [Database, "Queue depth", m?.queue_depth == null ? "Not instrumented" : String(m.queue_depth)],
              [MemoryStick, "Memory consumption", m?.memory_bytes == null ? "Not instrumented" : `${m.memory_bytes} bytes`]
            ].map(([Icon, label, value]) => {
              const RowIcon = Icon as typeof Clock3;
              return <div key={String(label)} className="flex items-center gap-3 rounded-xl border border-orange-950/[.06] bg-white/[.025] p-3"><span className="grid size-9 place-items-center rounded-lg bg-orange-950/[.04] text-stone-500"><RowIcon size={16} /></span><div className="min-w-0"><p className="text-xs text-stone-500">{String(label)}</p><p className="mt-0.5 text-sm font-medium text-stone-800">{String(value)}</p></div></div>;
            })}
          </div>
        </Card>
      </div>
    </div>
  );
}

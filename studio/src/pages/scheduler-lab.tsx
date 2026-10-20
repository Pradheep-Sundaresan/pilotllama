import { Activity, CircleGauge, Clock3, Layers3, Play, Rows3, ServerCog } from "lucide-react";
import type { ApiSnapshot } from "../lib/types";
import { formatMetric, formatTime } from "../lib/utils";
import { EmptyState } from "../components/empty-state";
import { MetricCard } from "../components/metric-card";
import { Badge } from "../components/ui/badge";
import { Card } from "../components/ui/card";

export function SchedulerLab({ snapshot }: { snapshot: ApiSnapshot }) {
  const scheduler = snapshot.scheduler;
  const utilization = scheduler && scheduler.queue_capacity > 0
    ? (scheduler.waiting_requests / scheduler.queue_capacity) * 100
    : null;
  const slots = scheduler ? Array.from({ length: scheduler.queue_capacity }, (_, index) => index < scheduler.waiting_requests) : [];
  const activeSlots = scheduler ? Array.from({ length: 2 }, (_, index) => index < scheduler.executing_requests) : [];

  return (
    <div className="space-y-5">
      <section className="relative overflow-hidden rounded-2xl border border-orange-400/15 bg-[#fff5eb] p-6 sm:p-8">
        <div className="absolute -right-24 -top-24 size-72 rounded-full bg-orange-500/[.08] blur-3xl" />
        <div className="relative flex flex-wrap items-start justify-between gap-5">
          <div><Badge className="border-orange-400/20 bg-orange-400/10 text-orange-700">Phase 6 · live snapshot</Badge><h2 className="mt-5 text-3xl font-semibold tracking-[-.04em] text-stone-900">Inside the scheduler.</h2><p className="mt-3 max-w-2xl text-sm leading-relaxed text-stone-600">Point-in-time occupancy measured inside the C++ runtime and transported through gRPC. Continuous mode can report two active requests; queue mode reports at most one.</p></div>
          <div className={`flex items-center gap-2 rounded-full border px-3 py-1.5 text-xs ${scheduler?.backend_state === "ready" ? "border-emerald-400/20 bg-emerald-400/10 text-emerald-700" : "border-orange-950/10 bg-white/70 text-stone-500"}`}><span className={`size-1.5 rounded-full ${scheduler?.backend_state === "ready" ? "animate-pulse bg-emerald-400" : "bg-zinc-600"}`} />{scheduler?.backend_state ?? "unavailable"}</div>
        </div>
      </section>

      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <MetricCard icon={Rows3} label="Waiting requests" value={scheduler ? String(scheduler.waiting_requests) : "Unavailable"} unit="requests" simple="People standing in line because every available model slot is busy." detail="Accepted request items not yet published as executing by the C++ worker. A dequeued item remains counted here until that transition is published." unavailable={!scheduler} />
        <MetricCard icon={Play} label="Executing requests" value={scheduler ? String(scheduler.executing_requests) : "Unavailable"} unit="requests" simple="Requests the model is actively working on right now." detail="Requests currently active in the C++ runtime. Queue mode reports at most one; continuous mode supports two active sequence slots." unavailable={!scheduler} />
        <MetricCard icon={Layers3} label="Waiting capacity" value={scheduler ? String(scheduler.queue_capacity) : "Unavailable"} unit="requests" simple="How many requests may wait in line before PilotLlama says it is full." detail="Maximum waiting request items in the bounded C++ queue. It excludes the currently executing request and does not measure bytes." unavailable={!scheduler} />
        <MetricCard icon={Clock3} label="Status RPC latency" value={formatMetric(scheduler?.status_rpc_latency_ms, 2)} unit="ms" simple="How long the dashboard took to ask the native engine for this status snapshot." detail="Go monotonic elapsed time for the localhost GetStatus RPC that carried this snapshot. It is control-plane observation latency, not generation latency." unavailable={!scheduler} />
      </div>

      <div className="grid gap-4 xl:grid-cols-[1.25fr_.75fr]">
        <Card className="p-5 sm:p-6">
          <div className="flex items-start justify-between gap-4"><div><p className="eyebrow">Bounded waiting queue</p><h3 className="mt-1 text-base font-semibold text-stone-900">Capacity occupancy</h3></div><Badge>{utilization == null ? "Unavailable" : `${formatMetric(utilization, 0)}% occupied`}</Badge></div>
          {scheduler ? <div className="mt-7 space-y-6"><div><p className="mb-2 text-[10px] uppercase tracking-[.13em] text-stone-400">Active sequence slots</p><div className="grid grid-cols-2 gap-3">{activeSlots.map((occupied, index) => <div key={index} className={`rounded-xl border p-3 ${occupied ? "border-emerald-400/25 bg-emerald-400/10" : "border-orange-950/[.06] bg-white/70"}`}><span className="text-[10px] text-stone-400">Sequence {index + 1}</span><div className={`mt-3 h-1.5 rounded-full ${occupied ? "bg-emerald-400 shadow-[0_0_12px_rgba(52,211,153,.55)]" : "bg-stone-200"}`}/><span className={`mt-2 block text-[10px] ${occupied ? "text-emerald-700" : "text-stone-400"}`}>{occupied ? "executing" : "available"}</span></div>)}</div></div><div><p className="mb-2 text-[10px] uppercase tracking-[.13em] text-stone-400">Waiting slots</p><div className="grid grid-cols-2 gap-3 sm:grid-cols-4">{slots.map((occupied, index) => <div key={index} className={`rounded-xl border p-3 ${occupied ? "border-orange-400/25 bg-orange-400/10" : "border-orange-950/[.06] bg-white/70"}`}><span className="text-[10px] text-stone-400">Slot {index + 1}</span><div className={`mt-3 h-1.5 rounded-full ${occupied ? "bg-orange-400 shadow-[0_0_12px_rgba(251,146,60,.65)]" : "bg-stone-200"}`}/><span className={`mt-2 block text-[10px] ${occupied ? "text-orange-700" : "text-stone-400"}`}>{occupied ? "waiting" : "available"}</span></div>)}</div></div></div> : <div className="mt-6"><EmptyState title="Scheduler measurement unavailable" detail="Start the updated backend and provide the configured bearer token. Older backends do not supply this snapshot." /></div>}
        </Card>

        <Card className="p-5 sm:p-6">
          <p className="eyebrow">Execution topology</p><h3 className="mt-1 text-base font-semibold text-stone-900">Current request path</h3>
          <div className="mt-6 space-y-3">{[
            [ServerCog, "Go admission", snapshot.admission?.state ?? "unavailable"],
            [Rows3, "C++ waiting queue", scheduler ? `${scheduler.waiting_requests} waiting` : "unavailable"],
            [CircleGauge, "C++ active sequences", scheduler ? `${scheduler.executing_requests} / 2 executing` : "unavailable"]
          ].map(([Icon, label, value], index) => { const RowIcon = Icon as typeof Activity; return <div key={String(label)}><div className="flex items-center gap-3 rounded-xl border border-orange-950/[.06] bg-white/[.025] p-3"><span className="grid size-9 place-items-center rounded-lg bg-orange-950/[.04] text-stone-500"><RowIcon size={16} /></span><div><p className="text-xs text-stone-500">{String(label)}</p><p className="mt-0.5 text-sm font-medium text-stone-800">{String(value)}</p></div></div>{index < 2 ? <div className="ml-7 h-3 w-px bg-white/10" /> : null}</div>; })}</div>
          <div className="mt-6 border-t border-orange-950/[.06] pt-4 text-[11px] leading-relaxed text-stone-400">Observed {scheduler ? formatTime(scheduler.observed_at_unix_ms) : "—"} at the Go service after a localhost status RPC. This is not a continuous trace, average, or historical maximum.</div>
        </Card>
      </div>
    </div>
  );
}

import { CircleHelp, type LucideIcon } from "lucide-react";
import { Card } from "./ui/card";
import { Tooltip } from "./ui/tooltip";

export function MetricCard({
  icon: Icon,
  label,
  value,
  unit,
  simple,
  detail,
  unavailable = false
}: {
  icon: LucideIcon;
  label: string;
  value: string;
  unit?: string;
  simple: string;
  detail: string;
  unavailable?: boolean;
}) {
  return (
    <Card className="group relative overflow-hidden p-5">
      <div className="absolute inset-x-0 top-0 h-px bg-gradient-to-r from-transparent via-orange-400/35 to-transparent opacity-0 transition-opacity group-hover:opacity-100" />
      <div className="mb-6 flex items-center justify-between">
        <span className="grid size-8 place-items-center rounded-lg border border-orange-950/[.07] bg-orange-950/[.035] text-stone-600">
          <Icon size={15} />
        </span>
        <Tooltip content={<div className="space-y-2"><div><p className="font-semibold text-stone-900">In plain English</p><p className="mt-0.5">{simple}</p></div><div className="border-t border-orange-950/10 pt-2"><p className="font-semibold text-stone-700">How it is measured</p><p className="mt-0.5 text-stone-500">{detail}</p></div></div>}>
          <button className="rounded-full p-1 text-stone-400 transition-colors hover:bg-orange-50 hover:text-orange-700" aria-label={`Explain ${label}`}>
            <CircleHelp size={14} />
          </button>
        </Tooltip>
      </div>
      <div className={unavailable ? "text-xl font-semibold text-stone-400" : "metric-value text-3xl font-semibold text-stone-900"}>
        {value}
        {unit && !unavailable ? <span className="ml-1.5 text-xs font-medium text-stone-500">{unit}</span> : null}
      </div>
      <div className="mt-2 text-xs font-medium uppercase tracking-[.13em] text-stone-500">{label}</div>
    </Card>
  );
}

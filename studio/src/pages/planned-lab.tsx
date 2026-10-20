import type { LucideIcon } from "lucide-react";
import { ArrowRight, Beaker, LockKeyhole } from "lucide-react";
import { Badge } from "../components/ui/badge";
import { Card } from "../components/ui/card";

export function PlannedLab({ icon: Icon, title, phase, description, planned }: { icon: LucideIcon; title: string; phase: string; description: string; planned: string[] }) {
  return (
    <div className="grid min-h-[calc(100vh-9rem)] place-items-center">
      <Card className="relative w-full max-w-3xl overflow-hidden p-8 sm:p-12">
        <div className="absolute -right-24 -top-24 size-72 rounded-full bg-orange-500/[.07] blur-3xl" />
        <div className="relative"><div className="flex items-center justify-between"><span className="grid size-12 place-items-center rounded-2xl border border-orange-400/20 bg-orange-400/10 text-orange-700"><Icon size={21} /></span><Badge><LockKeyhole size={10} className="mr-1" /> {phase}</Badge></div><h2 className="mt-8 text-3xl font-semibold tracking-[-.035em] text-stone-900">{title}</h2><p className="mt-3 max-w-xl text-sm leading-relaxed text-stone-500">{description}</p><div className="mt-8 grid gap-3 sm:grid-cols-2">{planned.map((item) => <div key={item} className="flex items-center gap-3 rounded-xl border border-orange-950/[.06] bg-white/[.025] p-4 text-sm text-stone-600"><Beaker size={15} className="shrink-0 text-stone-400" />{item}</div>)}</div><p className="mt-8 flex items-center gap-2 text-xs text-orange-700"><ArrowRight size={14} /> This page activates only when its backend measurements and experiments are implemented.</p></div>
      </Card>
    </div>
  );
}

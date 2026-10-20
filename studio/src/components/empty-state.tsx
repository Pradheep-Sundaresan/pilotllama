import { Activity } from "lucide-react";

export function EmptyState({ title, detail }: { title: string; detail: string }) {
  return (
    <div className="grid min-h-48 place-items-center rounded-xl border border-dashed border-orange-950/10 bg-orange-950/[.025] px-8 text-center">
      <div>
        <Activity className="mx-auto mb-3 text-stone-400" size={22} />
        <p className="text-sm font-medium text-stone-700">{title}</p>
        <p className="mx-auto mt-1 max-w-md text-xs leading-relaxed text-stone-400">{detail}</p>
      </div>
    </div>
  );
}

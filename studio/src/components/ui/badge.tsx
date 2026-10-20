import type { HTMLAttributes } from "react";
import { cn } from "../../lib/utils";

export function Badge({ className, ...props }: HTMLAttributes<HTMLSpanElement>) {
  return <span className={cn("inline-flex items-center rounded-full border border-orange-950/10 bg-white/70 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-[.14em] text-stone-500", className)} {...props} />;
}

import { BenchmarkLab } from "./benchmark-lab";
import { MemoryLab } from "./memory-lab";
import { SchedulerLab } from "./scheduler-lab";
import type { ApiSnapshot } from "../lib/types";

export function Experiments({ snapshot }: { snapshot: ApiSnapshot }) {
  return <div className="space-y-4">
    <BenchmarkLab/>
    <div className="console-disclosures">
      <details><summary>Live scheduler methodology</summary><div className="disclosure-body"><SchedulerLab snapshot={snapshot}/></div></details>
      <details><summary>Memory measurements</summary><div className="disclosure-body"><MemoryLab snapshot={snapshot}/></div></details>
    </div>
  </div>;
}

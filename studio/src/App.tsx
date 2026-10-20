import { useCallback, useEffect, useState } from "react";
import {
  ChartNoAxesCombined,
  ChevronRight,
  Cpu,
  KeyRound,
  Menu,
  MessageSquareText,
  RefreshCw,
  X
} from "lucide-react";
import { loadSnapshot } from "./lib/api";
import type { ApiSnapshot } from "./lib/types";
import { Playground } from "./pages/playground";
import { OperationsConsole } from "./pages/operations-console";
import { Experiments } from "./pages/experiments";
import { Badge } from "./components/ui/badge";
import { Button } from "./components/ui/button";

type Page = "console" | "playground" | "experiments";

const initialSnapshot: ApiSnapshot = {
  health: null, ready: null, backend: null, admission: null, metrics: null,
  history: null, scheduler: null, memory: null, models: [], modelConfig: null, error: null, refreshedAt: null
};

const navigation: { id: Page; label: string; icon: typeof ChartNoAxesCombined }[] = [
  { id: "console", label: "Console", icon: Cpu },
  { id: "playground", label: "Playground", icon: MessageSquareText },
  { id: "experiments", label: "Experiments", icon: ChartNoAxesCombined }
];

const titles: Record<Page, [string, string]> = {
  console: ["Operations Console", "Live model, scheduler, memory, requests and lifecycle"],
  playground: ["Playground", "Run a real request through Go, gRPC, and C++"],
  experiments: ["Experiments", "Measured scheduler, memory and batching evidence"]
};

export default function App() {
  const [page, setPage] = useState<Page>("console");
  const [token, setToken] = useState("");
  const [snapshot, setSnapshot] = useState(initialSnapshot);
  const [loading, setLoading] = useState(false);
  const [menu, setMenu] = useState(false);

  const refresh = useCallback(async () => {
    setLoading(true);
    try { setSnapshot(await loadSnapshot(token)); }
    finally { setLoading(false); }
  }, [token]);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), 5000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  function navigate(next: Page) { setPage(next); setMenu(false); }

  const content = {
    console: <OperationsConsole snapshot={snapshot} token={token} refresh={refresh} />,
    playground: <Playground snapshot={snapshot} token={token} onComplete={() => window.setTimeout(() => void refresh(), 250)} />,
    experiments: <Experiments snapshot={snapshot} />
  } satisfies Record<Page, React.ReactNode>;

  return (
    <div className="app-shell min-h-screen text-stone-900">
      {menu ? <button className="fixed inset-0 z-30 bg-stone-950/25 backdrop-blur-sm lg:hidden" aria-label="Close navigation" onClick={() => setMenu(false)} /> : null}
      <aside className={`fixed inset-y-0 left-0 z-40 flex w-[270px] flex-col border-r border-orange-950/10 bg-white/95 p-4 shadow-[10px_0_40px_rgba(121,63,28,.04)] backdrop-blur-xl transition-transform lg:translate-x-0 ${menu ? "translate-x-0" : "-translate-x-full"}`}>
        <div className="relative px-2 pt-2">
          <div className="sidebar-mascot"><img src="/pilotllama-hero.png" alt="PilotLlama mascot flying an orange propeller plane" /></div>
          <div className="mt-3 px-1"><p className="text-sm font-bold tracking-[.11em] text-stone-900">PILOTLLAMA</p><p className="mt-0.5 text-[10px] uppercase tracking-[.18em] text-stone-400">Local inference</p></div>
          <Button variant="ghost" size="icon" className="absolute right-1 top-1 lg:hidden" onClick={() => setMenu(false)}><X size={16} /></Button>
        </div>
        <nav className="mt-8 flex-1 space-y-1">
          <p className="mb-3 px-3 text-[9px] font-semibold uppercase tracking-[.2em] text-stone-400">Workspace</p>
          {navigation.map((item) => {
            const Icon = item.icon;
            return <button key={item.id} onClick={() => navigate(item.id)} className={`group flex w-full items-center gap-3 rounded-xl px-3 py-2.5 text-sm transition ${page === item.id ? "bg-orange-50/80 text-stone-900 shadow-sm ring-1 ring-orange-600/10" : "text-stone-500 hover:bg-stone-50 hover:text-stone-800"}`}><Icon size={16} className={page === item.id ? "text-[#df6235]" : "text-stone-400 group-hover:text-stone-600"} /><span className="flex-1 text-left">{item.label}</span>{page === item.id ? <ChevronRight size={13} className="text-[#df6235]" /> : null}</button>;
          })}
        </nav>
        <div className="rounded-xl border border-orange-950/10 bg-white/60 p-3">
          <div className="flex items-center justify-between"><span className="flex items-center gap-2 text-xs text-stone-500"><Cpu size={13} /> Local API</span><span className={`size-1.5 rounded-full ${snapshot.health ? "bg-emerald-500 shadow-[0_0_8px_#34d399]" : "bg-red-500"}`} /></div>
          <p className="mt-2 text-[11px] text-stone-400">127.0.0.1:8080</p>
        </div>
      </aside>

      <main className="lg:pl-[270px]">
        <header className="sticky top-0 z-20 flex min-h-20 items-center gap-4 border-b border-orange-950/[.08] bg-white/90 px-4 backdrop-blur-xl sm:px-7">
          <Button variant="ghost" size="icon" className="lg:hidden" onClick={() => setMenu(true)}><Menu size={18} /></Button>
          <div className="min-w-0 flex-1"><h1 className="text-base font-semibold text-stone-900">{titles[page][0]}</h1><p className="hidden truncate text-xs text-stone-500 sm:block">{titles[page][1]}</p></div>
          <label className="relative hidden md:block"><KeyRound className="absolute left-3 top-1/2 -translate-y-1/2 text-stone-400" size={14} /><input type="password" autoComplete="off" className="input h-9 w-52 pl-9 text-xs" placeholder="Bearer token (memory only)" value={token} onChange={(event) => setToken(event.target.value)} /></label>
          <Button variant="secondary" size="sm" onClick={refresh} disabled={loading}><RefreshCw size={13} className={loading ? "animate-spin" : ""} /><span className="hidden sm:inline">Refresh</span></Button>
          <Badge className={snapshot.ready?.ready ? "border-emerald-600/20 bg-emerald-50 text-emerald-700" : "border-red-600/20 bg-red-50 text-red-700"}>{snapshot.ready?.ready ? "Ready" : "Not ready"}</Badge>
        </header>
        <div className="border-b border-orange-950/[.06] p-3 md:hidden"><label className="relative block"><KeyRound className="absolute left-3 top-1/2 -translate-y-1/2 text-stone-400" size={14} /><input type="password" autoComplete="off" className="input h-9 pl-9 text-xs" placeholder="Bearer token · kept in memory only" value={token} onChange={(event) => setToken(event.target.value)} /></label></div>
        {snapshot.error ? <div className="mx-4 mt-4 rounded-xl border border-red-600/20 bg-red-50 px-4 py-3 text-xs text-red-700 sm:mx-7">{snapshot.error} Start the Go API, then refresh.</div> : null}
        <div className="p-4 sm:p-7">{content[page]}</div>
      </main>
    </div>
  );
}

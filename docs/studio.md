# Studio 0A — working dashboard foundation

Studio is a light, responsive React and TypeScript interface for the existing local
Go API. It uses Tailwind CSS, shadcn/ui-style local components backed by Radix UI,
Recharts, and Lucide icons. The approved npm dependencies are project-local under
`studio/node_modules`; exact versions and registry integrity hashes are recorded in
`studio/package-lock.json`. No system packages or settings are changed.

## Run and build

Start the Go API on its default loopback address, then:

```sh
cd studio
npm run dev
```

The production bundle is served by the Go API at `http://127.0.0.1:8080/` when the
service is launched through `./scripts/start.sh`. For hot-reload development, open
`http://127.0.0.1:5173`. The Vite development server proxies `/healthz`,
`/readyz`, `/v1`, and `/internal` to `http://127.0.0.1:8080`. It does not proxy other
paths or expose PilotLlama beyond loopback. `npm run build` runs strict TypeScript checks
and produces the ignored `studio/dist` bundle. The Go service validates and serves
that bundle from the same loopback origin as the API; it does not expose arbitrary
filesystem paths.

When API authentication is configured, enter its bearer token in Studio's header.
The token is held only in React memory: it is not written to local storage, cookies,
URLs, source files, or logs. Reloading the page clears it. Without configured auth,
generation can still work according to the Go configuration, but the internal
history, metrics, admission, and backend-control endpoints intentionally return 403.

## Implemented views

- **Overview** polls the bounded 60-second aggregate report and request history every
  five seconds. It shows real RPC TTFT, native decode rate, completion-attributed
  throughput, request outcomes, empirical quantiles, and recent handler latency.
- **Playground** discovers the live model alias and keeps a bounded multi-turn
  conversation with an optional leading system message. It supports both JSON and SSE
  output, cancellation, bounded temperature/top-p/top-k/seed/stop controls, token limits from 1 through the
  loaded runtime's configured output budget, and shows the returned request ID. A
  supported model can opt into a separate live reasoning panel.
- **Request Inspector** renders the retained 128-entry history without prompts or
  outputs. It keeps Go/RPC and native measurements separate and preserves nulls.
- **System Health** shows public health/readiness plus authenticated backend and
  admission state. Managed start/stop and drain/resume call the existing controls;
  the UI does not infer native idleness.
- **Scheduler Lab** shows real point-in-time C++ waiting, executing and capacity
  counts plus status-RPC latency and Go admission state. Phase 6D adds two explicit
  active sequence slots separately from the waiting queue.
- **Benchmark Lab** visualizes the real Phase 6C queue-versus-continuous A/B,
  including responsiveness, throughput, memory, sampled occupancy, provenance, and
  compact JSON/CSV downloads. It remains available while the API is offline.
- **Memory Lab** is active as of Phase 5D. It visualizes the live Phase 5B snapshot and
  a versioned copy of the real Phase 5C baseline, with JSON/CSV export. Process accounting
  and llama allocations remain separate, and unavailable readings stay unavailable.
- Model Manager remains an explicit planned state and contains no simulated results.

## Measurement boundaries

Studio does not calculate replacement performance metrics. It formats values supplied
by the API and labels their source and sample count. Aggregate throughput remains
completed native output tokens divided by the actual observation-window duration,
including idle time. Quantiles remain empirical nearest-rank values: P95 needs at
least 20 samples and P99 needs 100. RPC TTFT and native TTFT remain distinct; neither
is browser-visible TTFT. The aggregate queue-depth and memory fields remain **Not
instrumented**. Phase 5B's separate memory endpoint is rendered in Memory Lab but is
not a completion-window aggregate. Scheduler Lab labels current occupancy as a snapshot, not
a rolling queue-depth statistic. Benchmark Lab labels its plot as a 50 ms sampled
historical transition trace, not current occupancy or a continuous measurement. See
[scheduler methodology](scheduler-observability.md), [aggregate methodology](aggregate-observability.md),
[request fields](request-observability.md), and [native timing](observability.md).

## Verification

`npm run build` passed with strict TypeScript checking and a Vite production build.
The rendered application was checked at a compact viewport for responsive navigation,
Overview offline/empty states, Playground controls, Phase 6D Benchmark Lab charts,
queue/continuous evidence selector,
and accessible labels. npm reported
no known dependency vulnerabilities at installation time. No model was loaded and no
performance result was generated for this UI milestone.

"""Run a bounded queue-versus-continuous Inferno comparison on an existing GGUF."""

import argparse
import csv
from datetime import datetime, timezone
import importlib.util
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import sys
import time


ROOT = Path(__file__).resolve().parents[2]
BASELINE_PATH = Path(__file__).with_name("concurrency_baseline.py")
SPEC = importlib.util.spec_from_file_location("concurrency_baseline", BASELINE_PATH)
baseline = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(baseline)
DEFAULT_CONFIG = ROOT / "benchmarks/workloads/batching-comparison.json"


def stop_process(process):
    if process.poll() is not None:
        return
    process.send_signal(signal.SIGTERM)
    try:
        process.wait(timeout=20)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)
        raise RuntimeError("Inferno required forced termination")


def wait_ready(base_url, deadline):
    while time.monotonic() < deadline:
        try:
            ready = baseline.get_json(base_url, "/readyz", "", 2)
            if ready.get("ready"):
                return
        except Exception:
            pass
        time.sleep(0.1)
    raise RuntimeError("Inferno did not become ready within the startup budget")


def host_observation():
    memory = baseline.command_output(["memory_pressure"])
    thermal = baseline.command_output(["pmset", "-g", "therm"])
    return {"memory_pressure": memory, "thermal": thermal,
            "thermal_available": bool(thermal and "CPU_Scheduler_Limit" in thermal)}


def write_comparison(path, manifests):
    fields = ["mode", "concurrency", "requests", "completed", "errors",
              "throughput_tokens_per_second", "ttft_p50_ms", "latency_p50_ms",
              "native_queue_wait_p50_ms", "sampled_max_waiting", "sampled_max_executing",
              "completed_output_sha256"]
    rows = []
    for mode, manifest in manifests.items():
        for case in manifest["cases"]:
            summary = case["summary"]
            digests = sorted({row["content_sha256"] for row in case["requests"]
                              if row["outcome"] == "completed"})
            rows.append({"mode": mode, "concurrency": case["concurrency"],
                         "requests": summary["requests"], "completed": summary["client_completed"],
                         "errors": summary["client_failed_or_incomplete"],
                         "throughput_tokens_per_second": summary["completion_attributed_throughput_tokens_per_second"],
                         "ttft_p50_ms": summary["client_ttft_ms"]["p50"],
                         "latency_p50_ms": summary["client_latency_ms"]["p50"],
                         "native_queue_wait_p50_ms": summary["native_queue_wait_ms"]["p50"],
                         "sampled_max_waiting": summary["sampled_max_waiting_requests"],
                         "sampled_max_executing": summary["sampled_max_executing_requests"],
                         "completed_output_sha256": ";".join(digests)})
    with path.open("w", newline="") as stream:
        writer = csv.DictWriter(stream, fields)
        writer.writeheader()
        writer.writerows(rows)
    return rows


def run(model, config, output, api, server):
    if output.exists():
        raise ValueError("Output directory must not already exist")
    output.mkdir(parents=True)
    token = secrets.token_urlsafe(32)
    env = os.environ.copy()
    env["INFERNO_PHASE6C_TOKEN"] = token
    base_url = "http://127.0.0.1:18080"
    manifests = {}
    comparison = {"schema_version": 1, "status": "running",
                  "benchmark": "inferno_queue_vs_continuous", "started_at": datetime.now(timezone.utc).isoformat(),
                  "modes": ["queue", "continuous"], "host_before": host_observation()}
    try:
        for mode in comparison["modes"]:
            log_path = output / f"{mode}.log"
            with log_path.open("w") as log:
                process = subprocess.Popen([
                    str(api), "--listen", "127.0.0.1:18080", "--backend-exec", str(server),
                    "--model", str(model), "--model-id", config["model"],
                    "--backend-batching", mode, "--auth-token-env", "INFERNO_PHASE6C_TOKEN",
                    "--request-timeout", "120s", "--rate-per-minute", "0",
                    "--backend-state-dir", str(output / f"state-{mode}"),
                ], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
                try:
                    wait_ready(base_url, time.monotonic() + 45)
                    manifests[mode] = baseline.run(config, output / mode, base_url, token,
                                                   "generated_ephemeral_not_recorded")
                finally:
                    stop_process(process)
            time.sleep(1)
        comparison["rows"] = write_comparison(output / "comparison.csv", manifests)
        comparison["status"] = "complete"
    except BaseException as exception:
        comparison["status"] = "failed"
        comparison["error"] = {"kind": type(exception).__name__, "message": str(exception)}
        raise
    finally:
        comparison["host_after"] = host_observation()
        comparison["finished_at"] = datetime.now(timezone.utc).isoformat()
        (output / "comparison.json").write_text(json.dumps(comparison, indent=2) + "\n")
    return comparison


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("model", type=Path)
    parser.add_argument("--config", type=Path, default=DEFAULT_CONFIG)
    parser.add_argument("--api", type=Path, default=ROOT / "build/inferno-api")
    parser.add_argument("--server", type=Path, default=ROOT / "build/grpc/inferno_rpc_server")
    parser.add_argument("--output", type=Path, default=ROOT / "benchmarks/results" /
                        ("batching-" + datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")))
    args = parser.parse_args()
    paths = [path.expanduser().resolve() for path in (args.model, args.config, args.api, args.server, args.output)]
    model, config_path, api, server, output = paths
    if not output.is_relative_to(ROOT) or not model.is_file() or not config_path.is_file():
        parser.error("Use an existing model/config and a project-local output directory")
    if not api.is_file() or not server.is_file():
        parser.error("Build inferno-api and inferno_rpc_server first")
    try:
        result = run(model, json.loads(config_path.read_text()), output, api, server)
    except (OSError, ValueError, RuntimeError, KeyError, json.JSONDecodeError) as exception:
        print(f"Benchmark failed: {exception}", file=sys.stderr)
        return 1
    for row in result["rows"]:
        print(f"{row['mode']} c={row['concurrency']}: {row['completed']}/{row['requests']} "
              f"completed, {row['throughput_tokens_per_second']:.2f} tok/s")
    print(f"Results: {output}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

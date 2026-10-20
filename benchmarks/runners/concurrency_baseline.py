"""Reproducible localhost concurrency benchmark for the Inferno public API."""

import argparse
import concurrent.futures
import csv
from datetime import datetime, timezone
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import subprocess
import sys
import threading
import time
from urllib import error, parse, request


ROOT = Path(__file__).resolve().parents[2]
DEFAULT_CONFIG = ROOT / "benchmarks/workloads/concurrency.json"


def validate_config(config):
    required = {
        "model", "model_artifact_sha256", "quantization", "prompt", "max_tokens",
        "concurrency_levels", "repetitions_per_client", "request_timeout_seconds",
        "history_settle_timeout_seconds", "scheduler_poll_interval_ms", "warmup_requests",
        "runtime_declared",
    }
    if set(config) != required:
        raise ValueError("Configuration keys must match the concurrency workload schema")
    for key in ("model", "quantization", "prompt"):
        if not isinstance(config[key], str) or not config[key]:
            raise ValueError(f"{key} must be a non-empty string")
    digest = config["model_artifact_sha256"]
    if not isinstance(digest, str) or len(digest) != 64 or any(c not in "0123456789abcdef" for c in digest):
        raise ValueError("model_artifact_sha256 must be a lowercase SHA-256 digest")
    if len(config["model"]) > 256 or len(config["prompt"].encode("utf-8")) > 65536:
        raise ValueError("Model or prompt exceeds the public API bound")
    if "<|im_start|>" in config["prompt"] or "<|im_end|>" in config["prompt"]:
        raise ValueError("Prompt contains a reserved template delimiter")
    integers = {
        "max_tokens": (1, 512),
        "repetitions_per_client": (1, 10),
        "request_timeout_seconds": (1, 3600),
        "history_settle_timeout_seconds": (1, 30),
        "scheduler_poll_interval_ms": (20, 1000),
        "warmup_requests": (1, 3),
    }
    for key, (low, high) in integers.items():
        value = config[key]
        if type(value) is not int or not low <= value <= high:
            raise ValueError(f"{key} must be an integer from {low} to {high}")
    levels = config["concurrency_levels"]
    if (not isinstance(levels, list) or not levels or len(levels) > 4 or
            any(type(value) is not int or not 1 <= value <= 4 for value in levels) or
            len(levels) != len(set(levels))):
        raise ValueError("concurrency_levels must contain unique integers from 1 to 4")
    runtime = config["runtime_declared"]
    runtime_keys = {"native_workers", "native_waiting_queue_capacity", "public_generation_slots",
                    "context_tokens", "batch_size", "threads", "gpu_layers_requested",
                    "go_rate_per_minute", "go_rate_burst", "go_request_timeout_seconds"}
    zero_allowed = {"gpu_layers_requested", "go_rate_per_minute", "go_rate_burst"}
    if (not isinstance(runtime, dict) or set(runtime) != runtime_keys or
            any(type(value) is not int or value < 0 for value in runtime.values()) or
            any(runtime[key] == 0 for key in runtime_keys - zero_allowed)):
        raise ValueError("runtime_declared must contain the bounded native launch settings")


def validate_base_url(value):
    parsed = parse.urlsplit(value)
    if (parsed.scheme != "http" or parsed.hostname not in {"127.0.0.1", "::1"} or
            parsed.port is None or parsed.username or parsed.password or
            parsed.path.rstrip("/") or parsed.query or parsed.fragment):
        raise ValueError("base URL must be an explicit loopback HTTP origin with a port")
    return value.rstrip("/")


def command_output(command):
    try:
        result = subprocess.run(command, capture_output=True, text=True, timeout=10)
    except (OSError, subprocess.SubprocessError):
        return None
    return result.stdout.strip() if result.returncode == 0 else None


def opener():
    return request.build_opener(request.ProxyHandler({}))


def auth_headers(token, json_body=False):
    headers = {"Authorization": f"Bearer {token}", "Connection": "close"}
    if json_body:
        headers["Content-Type"] = "application/json"
    return headers


def get_json(base_url, path, token, timeout):
    req = request.Request(base_url + path, headers=auth_headers(token))
    with opener().open(req, timeout=timeout) as response:
        return json.load(response)


def error_detail(exception):
    if isinstance(exception, error.HTTPError):
        code = None
        try:
            body = json.loads(exception.read().decode("utf-8"))
            code = body.get("error", {}).get("code")
        except (UnicodeDecodeError, json.JSONDecodeError, AttributeError):
            pass
        return {"kind": "http", "http_status": exception.code, "api_code": code}
    return {"kind": type(exception).__name__}


def consume_sse(response, started_ns):
    first_content_ms = None
    content_bytes = 0
    content_events = 0
    saw_done = False
    stream_error = None
    pending = []
    content_hash = hashlib.sha256()
    for raw_line in response:
        line = raw_line.decode("utf-8")
        if line not in {"\n", "\r\n"}:
            pending.append(line.rstrip("\r\n"))
            continue
        data = "\n".join(part[5:].strip() for part in pending if part.startswith("data:"))
        pending.clear()
        if not data:
            continue
        if data == "[DONE]":
            saw_done = True
            continue
        event = json.loads(data)
        if "error" in event:
            stream_error = event["error"].get("code", "unknown")
            continue
        choices = event.get("choices", [])
        content = choices[0].get("delta", {}).get("content") if choices else None
        if isinstance(content, str) and content:
            if first_content_ms is None:
                first_content_ms = (time.perf_counter_ns() - started_ns) / 1e6
            content_events += 1
            encoded = content.encode("utf-8")
            content_bytes += len(encoded)
            content_hash.update(encoded)
    return first_content_ms, content_events, content_bytes, content_hash.hexdigest(), saw_done, stream_error


def stream_request(base_url, token, config, gate):
    gate.wait(timeout=10)
    payload = json.dumps({
        "model": config["model"],
        "messages": [{"role": "user", "content": config["prompt"]}],
        "max_tokens": config["max_tokens"], "temperature": 0, "n": 1, "stream": True,
    }, separators=(",", ":")).encode("utf-8")
    started_ns = time.perf_counter_ns()
    request_id = None
    try:
        req = request.Request(base_url + "/v1/chat/completions", data=payload,
                              headers=auth_headers(token, True), method="POST")
        with opener().open(req, timeout=config["request_timeout_seconds"]) as response:
            headers_ms = (time.perf_counter_ns() - started_ns) / 1e6
            request_id = response.headers.get("X-Request-ID")
            ttft_ms, events, byte_count, content_sha256, done, stream_error = consume_sse(response, started_ns)
        latency_ms = (time.perf_counter_ns() - started_ns) / 1e6
        outcome = "completed" if done and stream_error is None else ("failed" if stream_error else "incomplete")
        return {"request_id": request_id, "outcome": outcome,
                "http_status": 200, "response_headers_ms": headers_ms,
                "client_ttft_ms": ttft_ms, "client_latency_ms": latency_ms,
                "content_events": events, "content_bytes": byte_count,
                "content_sha256": content_sha256,
                "saw_done": done, "error": ({"kind": "stream", "api_code": stream_error}
                                                if stream_error else None)}
    except Exception as exception:
        if isinstance(exception, error.HTTPError):
            request_id = exception.headers.get("X-Request-ID")
        return {"request_id": request_id, "outcome": "failed", "http_status": getattr(exception, "code", None),
                "response_headers_ms": None, "client_ttft_ms": None,
                "client_latency_ms": (time.perf_counter_ns() - started_ns) / 1e6,
                "content_events": 0, "content_bytes": 0, "saw_done": False,
                "content_sha256": None,
                "error": error_detail(exception)}


def correlate(base_url, token, observation, config):
    request_id = observation.get("request_id")
    if not request_id:
        observation["server_record"] = None
        observation["correlation"] = "no_request_id"
        return
    deadline = time.monotonic() + config["history_settle_timeout_seconds"]
    while True:
        try:
            observation["server_record"] = get_json(
                base_url, f"/internal/v1/requests/{request_id}", token,
                min(5, config["history_settle_timeout_seconds"]))
            observation["correlation"] = "matched"
            return
        except error.HTTPError as exception:
            if exception.code != 404 or time.monotonic() >= deadline:
                observation["server_record"] = None
                observation["correlation"] = "not_retained" if exception.code == 404 else "lookup_failed"
                return
        except Exception:
            observation["server_record"] = None
            observation["correlation"] = "lookup_failed"
            return
        time.sleep(0.025)


def sample_scheduler(base_url, token, config, case_started_ns, stop, samples, failures):
    interval = config["scheduler_poll_interval_ms"] / 1000
    while True:
        started_ns = time.perf_counter_ns()
        try:
            snapshot = get_json(base_url, "/internal/v1/scheduler", token,
                                min(5, config["request_timeout_seconds"]))
            snapshot["client_observed_offset_ms"] = (time.perf_counter_ns() - case_started_ns) / 1e6
            samples.append(snapshot)
        except Exception as exception:
            failures.append({"client_observed_offset_ms": (time.perf_counter_ns() - case_started_ns) / 1e6,
                             "error": error_detail(exception)})
        if stop.wait(max(0, interval - (time.perf_counter_ns() - started_ns) / 1e9)):
            return


def nearest_rank(values, percentile):
    if not values:
        return None
    ordered = sorted(values)
    return ordered[max(0, math.ceil(percentile * len(ordered)) - 1)]


def distribution(values):
    values = [value for value in values if isinstance(value, (int, float)) and not isinstance(value, bool)]
    return {"samples": len(values), "p50": nearest_rank(values, 0.50),
            "p95": nearest_rank(values, 0.95) if len(values) >= 20 else None,
            "p99": nearest_rank(values, 0.99) if len(values) >= 100 else None}


def summarize_case(case):
    observations = case["requests"]
    matched = [item["server_record"] for item in observations if item.get("server_record")]
    completed_native = [row for row in matched if row.get("outcome") == "completed" and row.get("native")]
    samples = case["scheduler_samples"]
    duration_s = case["case_duration_ms"] / 1000
    native_tokens = sum(row["native"].get("output_tokens", 0) for row in completed_native)
    return {
        "requests": len(observations),
        "client_completed": sum(item["outcome"] == "completed" for item in observations),
        "client_failed_or_incomplete": sum(item["outcome"] != "completed" for item in observations),
        "server_records_matched": len(matched),
        "completed_native_output_tokens": native_tokens,
        "completion_attributed_throughput_tokens_per_second": native_tokens / duration_s if duration_s > 0 else None,
        "client_ttft_ms": distribution([item["client_ttft_ms"] for item in observations]),
        "client_latency_ms": distribution([item["client_latency_ms"] for item in observations]),
        "server_handler_latency_ms": distribution([row.get("handler_latency_ms") for row in matched]),
        "native_queue_wait_ms": distribution([row["native"].get("queue_wait_ms") for row in completed_native]),
        "scheduler_sample_count": len(samples),
        "scheduler_failure_count": len(case["scheduler_failures"]),
        "sampled_max_waiting_requests": max((row["waiting_requests"] for row in samples), default=None),
        "sampled_max_executing_requests": max((row["executing_requests"] for row in samples), default=None),
        "sampled_busy_fraction": (sum(row["executing_requests"] > 0 for row in samples) / len(samples)) if samples else None,
    }


def run_case(base_url, token, config, concurrency):
    stop = threading.Event()
    samples, sample_failures = [], []
    case_started_ns = time.perf_counter_ns()
    sampler = threading.Thread(target=sample_scheduler,
                               args=(base_url, token, config, case_started_ns, stop, samples, sample_failures))
    sampler.start()
    gate = threading.Barrier(concurrency + 1)

    def client(client_index):
        rows = []
        for repetition in range(config["repetitions_per_client"]):
            row = stream_request(base_url, token, config, gate if repetition == 0 else _OpenGate())
            row.update(client_index=client_index, repetition=repetition + 1)
            correlate(base_url, token, row, config)
            rows.append(row)
        return rows

    try:
        with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
            futures = [pool.submit(client, index + 1) for index in range(concurrency)]
            gate.wait(timeout=10)
            observations = [row for future in futures for row in future.result()]
    finally:
        stop.set()
        sampler.join(timeout=6)
        if sampler.is_alive():
            raise RuntimeError("scheduler sampler did not stop")
    case = {"concurrency": concurrency, "repetitions_per_client": config["repetitions_per_client"],
            "case_duration_ms": (time.perf_counter_ns() - case_started_ns) / 1e6,
            "requests": observations, "scheduler_samples": samples,
            "scheduler_failures": sample_failures}
    case["summary"] = summarize_case(case)
    return case


class _OpenGate:
    @staticmethod
    def wait(timeout=None):
        return 0


def preflight(base_url, token, config):
    ready = get_json(base_url, "/readyz", token, 5)
    models = get_json(base_url, "/v1/models", token, 5)
    scheduler = get_json(base_url, "/internal/v1/scheduler", token, 5)
    ids = [row.get("id") for row in models.get("data", [])]
    if not ready.get("ready") or config["model"] not in ids:
        raise ValueError("Inferno backend is not ready with the configured model")
    if scheduler.get("source") != "cpp_runtime_status_snapshot" or scheduler.get("unit") != "requests":
        raise ValueError("Scheduler endpoint did not return the documented native snapshot")
    return {"ready": ready, "models": ids, "scheduler": scheduler}


def optional_memory(base_url, token):
    try:
        return {"status": "measured", "response": get_json(base_url, "/internal/v1/memory", token, 5)}
    except Exception as exception:
        return {"status": "unavailable", "reason": error_detail(exception)}


def write_csv(path, cases):
    fields = [
        "concurrency", "repetitions_per_client", "case_duration_ms", "requests",
        "client_completed", "client_failed_or_incomplete", "server_records_matched",
        "completed_native_output_tokens", "completion_attributed_throughput_tokens_per_second",
        "client_ttft_samples", "client_ttft_p50_ms", "client_ttft_p95_ms", "client_ttft_p99_ms",
        "client_latency_samples", "client_latency_p50_ms", "client_latency_p95_ms", "client_latency_p99_ms",
        "native_queue_wait_p50_ms", "scheduler_sample_count", "scheduler_failure_count",
        "sampled_max_waiting_requests", "sampled_max_executing_requests", "sampled_busy_fraction",
    ]
    with path.open("w", newline="") as stream:
        writer = csv.DictWriter(stream, fields)
        writer.writeheader()
        for case in cases:
            summary = case["summary"]
            writer.writerow({
                "concurrency": case["concurrency"], "repetitions_per_client": case["repetitions_per_client"],
                "case_duration_ms": case["case_duration_ms"], "requests": summary["requests"],
                **{key: summary[key] for key in fields if key in summary},
                **{f"client_ttft_{key}_ms" if key != "samples" else "client_ttft_samples": value
                   for key, value in summary["client_ttft_ms"].items()},
                **{f"client_latency_{key}_ms" if key != "samples" else "client_latency_samples": value
                   for key, value in summary["client_latency_ms"].items()},
                "native_queue_wait_p50_ms": summary["native_queue_wait_ms"]["p50"],
            })


def run(config, output, base_url, token, token_env):
    validate_config(config)
    base_url = validate_base_url(base_url)
    if output.exists():
        raise ValueError("Output directory must not already exist")
    check = preflight(base_url, token, config)
    output.mkdir(parents=True)
    manifest = {
        "schema_version": 1, "status": "running", "started_at": datetime.now(timezone.utc).isoformat(),
        "benchmark": "inferno_public_api_concurrency", "base_url": base_url,
        "credential": {"source_environment_variable": token_env, "value_recorded": False},
        "workload": config, "model_identity": {
            "api_model_id": config["model"], "quantization_declared": config["quantization"],
            "artifact_sha256_declared": config["model_artifact_sha256"],
            "serving_process_artifact_verification": "unavailable",
        },
        "runtime": {"configuration_declared": config["runtime_declared"],
                    "llama_cpp_revision_pinned": (ROOT / "third_party/llama.cpp-revision.txt").read_text().strip(),
                    "inferno_revision": command_output(["git", "-C", str(ROOT), "rev-parse", "HEAD"])},
        "memory_measurement": {"source": "authenticated /internal/v1/memory snapshots",
                               "unit": "bytes", "before": optional_memory(base_url, token)},
        "host": {"architecture": platform.machine(), "os": platform.platform(),
                 "macos": platform.mac_ver()[0], "python": platform.python_version(),
                 "cpu": command_output(["sysctl", "-n", "machdep.cpu.brand_string"]),
                 "memory_bytes": command_output(["sysctl", "-n", "hw.memsize"]),
                 "logical_cpus": os.cpu_count()},
        "preflight": check, "cases": [],
        "warmup": {"method": "serial public streaming requests before measured cases",
                   "included_in_case_summaries": False, "requests": []},
    }
    try:
        for index in range(config["warmup_requests"]):
            observation = stream_request(base_url, token, config, _OpenGate())
            observation.update(warmup_index=index + 1)
            correlate(base_url, token, observation, config)
            manifest["warmup"]["requests"].append(observation)
            if observation["outcome"] != "completed" or observation["correlation"] != "matched":
                raise RuntimeError("Warm-up request did not complete with a correlated server record")
        for concurrency in config["concurrency_levels"]:
            case = run_case(base_url, token, config, concurrency)
            manifest["cases"].append(case)
            print(f"concurrency {concurrency}: {case['summary']['client_completed']}/{case['summary']['requests']} completed", flush=True)
        manifest["status"] = "complete"
    except (Exception, KeyboardInterrupt) as exception:
        manifest["status"] = "failed"
        manifest["error"] = {"kind": type(exception).__name__, "message": str(exception)}
        raise
    finally:
        manifest["memory_measurement"]["after"] = optional_memory(base_url, token)
        manifest["finished_at"] = datetime.now(timezone.utc).isoformat()
        (output / "results.json").write_text(json.dumps(manifest, indent=2, allow_nan=False) + "\n")
    write_csv(output / "summary.csv", manifest["cases"])
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=DEFAULT_CONFIG)
    parser.add_argument("--base-url", default="http://127.0.0.1:8080")
    parser.add_argument("--auth-token-env", default="INFERNO_AUTH_TOKEN")
    parser.add_argument("--output", type=Path, default=ROOT / "benchmarks/results" /
                        ("concurrency-" + datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")))
    args = parser.parse_args()
    output = args.output.expanduser().resolve()
    if not output.is_relative_to(ROOT):
        parser.error("Output must remain inside the project")
    token = os.environ.get(args.auth_token_env, "")
    if not token:
        parser.error(f"{args.auth_token_env} must contain the existing Inferno API token")
    try:
        run(json.loads(args.config.read_text()), output, args.base_url, token, args.auth_token_env)
    except (OSError, ValueError, KeyError, RuntimeError, json.JSONDecodeError) as exception:
        print(f"Benchmark failed: {exception}", file=sys.stderr)
        return 1
    print(f"Results: {output}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

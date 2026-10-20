"""Offline, single-process llama-bench baselines; Python 3.12 standard library only."""

import argparse
import csv
from datetime import datetime, timezone
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import re
import signal
import statistics
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[2]


def sha256(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def validate_config(config):
    required = {"prompt_tokens", "decode_tokens", "repetitions", "batch_size",
                "microbatch_size", "threads", "gpu_layers", "timeout_seconds"}
    if not required <= set(config) or set(config) - required - {"decode_depths"}:
        raise ValueError("Configuration keys must match the baseline workload schema")
    for key in required - {"prompt_tokens", "decode_tokens"}:
        if type(config[key]) is not int or config[key] <= 0:
            raise ValueError(f"{key} must be a positive integer")
    for key in ("prompt_tokens", "decode_tokens"):
        values = config[key]
        if (not isinstance(values, list) or len(values) > 8 or
                any(type(n) is not int or not 1 <= n <= 4096 for n in values) or
                len(set(values)) != len(values)):
            raise ValueError(f"{key} must contain unique integers from 1 to 4096")
    depths = config.get("decode_depths", [0])
    if (not isinstance(depths, list) or not depths or len(depths) > 8 or
            any(type(n) is not int or not 0 <= n <= 4096 for n in depths) or
            len(set(depths)) != len(depths)):
        raise ValueError("decode_depths must contain unique integers from 0 to 4096")
    if not config["prompt_tokens"] and not config["decode_tokens"]:
        raise ValueError("At least one prefill or decode workload is required")
    if any(depth + tokens > 4096 for depth in depths for tokens in config["decode_tokens"]):
        raise ValueError("Decode depth plus generated steps must not exceed 4096")
    if not 2 <= config["repetitions"] <= 20:
        raise ValueError("Use 2-20 repetitions for this small baseline runner")
    if config["microbatch_size"] > config["batch_size"]:
        raise ValueError("microbatch_size must not exceed batch_size")


def summarize(raw, kind, tokens, repetitions, commit, depth=0):
    if not isinstance(raw, list) or len(raw) != 1:
        raise ValueError("Expected exactly one upstream result per invocation")
    row = raw[0]
    expected = (tokens, 0) if kind == "prefill" else (0, tokens)
    if (row["n_prompt"], row["n_gen"]) != expected or row["n_depth"] != depth:
        raise ValueError("Upstream workload differs from the requested workload")
    if not row["build_commit"] or not commit.startswith(row["build_commit"]):
        raise ValueError("Binary build commit differs from the pinned checkout")
    samples = row["samples_ns"]
    if len(samples) != repetitions or any(type(n) is not int or n <= 0 for n in samples):
        raise ValueError("Invalid or incomplete upstream timing samples")
    rates = [tokens * 1e9 / n for n in samples]
    for actual, expected_rate in zip(row["samples_ts"], rates, strict=True):
        if not math.isclose(actual, expected_rate, rel_tol=1e-5, abs_tol=1e-5):
            raise ValueError("Upstream token rates disagree with timing samples")
    return {
        "kind": kind, "tokens_per_repetition": tokens, "sample_count": len(samples),
        "context_depth_tokens": depth, "context_tokens_requested": tokens + depth,
        "mean_tokens_per_second": statistics.mean(rates),
        "stdev_tokens_per_second": statistics.stdev(rates),
        "mean_duration_ms": statistics.mean(samples) / 1e6,
        "samples_ns": samples,
    }


def peak_rss(log):
    match = re.search(r"^\s*(\d+)\s+maximum resident set size\s*$", log, re.MULTILINE)
    return int(match[1]) if match else None


def cases(config):
    return ([("prefill", tokens, 0) for tokens in config["prompt_tokens"]] +
            [("decode", tokens, depth) for tokens in config["decode_tokens"]
             for depth in config.get("decode_depths", [0])])


def command_output(command):
    result = subprocess.run(command, capture_output=True, text=True, timeout=10)
    return result.stdout.strip() if result.returncode == 0 else None


def execute(command, directory, timeout):
    """Keep evidence on failure and terminate the entire child group on timeout."""
    with (directory / "stdout.json").open("w") as out, (directory / "stderr.log").open("w") as err:
        with subprocess.Popen(command, stdout=out, stderr=err, start_new_session=True) as child:
            try:
                code = child.wait(timeout=timeout)
            except (subprocess.TimeoutExpired, KeyboardInterrupt):
                os.killpg(child.pid, signal.SIGTERM)
                try:
                    child.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    os.killpg(child.pid, signal.SIGKILL)
                    child.wait()
                raise
    if code != 0:
        raise RuntimeError(f"llama-bench exited {code}; inspect {directory / 'stderr.log'}")


def run(config, output, model):
    if platform.system() != "Darwin" or platform.machine() != "arm64":
        raise ValueError("This initial runner targets native Apple Silicon macOS")
    validate_config(config)
    source = ROOT / "third_party/llama.cpp"
    commit = (ROOT / "third_party/llama.cpp-revision.txt").read_text().strip()
    if command_output(["git", "-C", str(source), "rev-parse", "HEAD"]) != commit:
        raise ValueError("Upstream checkout does not match the revision pin")
    status = subprocess.run(["git", "-C", str(source), "status", "--porcelain"],
                            capture_output=True, text=True, check=True)
    if status.stdout:
        raise ValueError("Upstream checkout must be clean")
    binary = ROOT / "build/llama-metal/bin/llama-bench"
    binary_hash = sha256(binary)
    model_hash = sha256(model)
    if model_hash != MODEL_HASH:
        raise ValueError("Phase 1A requires the documented Nemotron Q4_K_M artifact")
    cache = (ROOT / "build/llama-metal/CMakeCache.txt").read_text()
    output.mkdir(parents=True, exist_ok=False)
    manifest = {
        "schema_version": 2, "status": "running",
        "started_at": datetime.now(timezone.utc).isoformat(),
        "configuration": config, "concurrency": 1,
        "workload": "upstream synthetic token IDs; no natural-language dataset or sampling",
        "warmup": "upstream default: full prompt warmup or one decode step",
        "depth_preparation": "first repetition evaluates depth, later repetitions restore state; outside sample timer",
        "model": {"id": "nvidia/NVIDIA-Nemotron-3-Nano-4B-BF16",
                  "quantization": "Q4_K_M", "path": str(model), "sha256": model_hash,
                  "bytes": model.stat().st_size, "original_revision": None},
        "runtime": {"commit": commit, "binary_sha256": binary_hash,
                    "binary": str(binary), "inferno_revision": command_output(
                        ["git", "-C", str(ROOT), "rev-parse", "HEAD"])},
        "host": {"architecture": platform.machine(), "os": platform.platform(),
                 "macos": platform.mac_ver()[0], "python": platform.python_version(),
                 "cpu": command_output(["sysctl", "-n", "machdep.cpu.brand_string"]),
                 "memory_bytes": command_output(["sysctl", "-n", "hw.memsize"]),
                 "logical_cpus": os.cpu_count()},
        "results": [],
    }
    manifest["runtime"]["cmake_settings"] = dict(re.findall(
        r"^((?:GGML_|LLAMA_|CMAKE_BUILD_TYPE|CMAKE_CXX_COMPILER)[^:=]*):[^=]*=(.*)$",
        cache, re.MULTILINE))
    try:
        for kind, tokens, depth in cases(config):
            suffix = f"-depth-{depth}" if depth else ""
            directory = output / f"{kind}-{tokens}{suffix}"
            directory.mkdir()
            command = ["/usr/bin/time", "-l", str(binary), "-m", str(model), "--offline",
                       "-p", str(tokens if kind == "prefill" else 0),
                       "-n", str(tokens if kind == "decode" else 0), "-d", str(depth),
                       "-r", str(config["repetitions"]), "-b", str(config["batch_size"]),
                       "-ub", str(config["microbatch_size"]), "-t", str(config["threads"]),
                       "-ngl", str(config["gpu_layers"]), "--poll", "0", "-o", "json"]
            manifest["active_command"] = command
            (directory / "command.json").write_text(json.dumps(command, indent=2) + "\n")
            started = time.monotonic()
            execute(command, directory, config["timeout_seconds"])
            process_wall_seconds = time.monotonic() - started
            raw = json.loads((directory / "stdout.json").read_text())
            result = summarize(raw, kind, tokens, config["repetitions"], commit, depth)
            result.update(command=command, process_wall_seconds=process_wall_seconds,
                          peak_process_rss_bytes=peak_rss((directory / "stderr.log").read_text()),
                          upstream=raw[0])
            manifest["results"].append(result)
            print(f"{kind} {tokens}, depth {depth}: {result['mean_tokens_per_second']:.2f} tokens/s", flush=True)
        manifest["status"] = "complete"
    except (Exception, KeyboardInterrupt) as error:
        manifest["status"] = "failed"
        manifest["error"] = f"{type(error).__name__}: {error}"
        raise
    finally:
        manifest["finished_at"] = datetime.now(timezone.utc).isoformat()
        (output / "results.json").write_text(json.dumps(manifest, indent=2, allow_nan=False) + "\n")
    fields = ["kind", "tokens_per_repetition", "sample_count", "context_depth_tokens", "context_tokens_requested",
              "mean_tokens_per_second", "stdev_tokens_per_second", "mean_duration_ms",
              "process_wall_seconds", "peak_process_rss_bytes"]
    with (output / "summary.csv").open("w", newline="") as stream:
        writer = csv.DictWriter(stream, fields, extrasaction="ignore")
        writer.writeheader()
        writer.writerows(manifest["results"])
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=ROOT / "benchmarks/workloads/baseline.json")
    parser.add_argument("--model", type=Path, required=True,
                        help="absolute path to an existing GGUF; no model is downloaded")
    parser.add_argument("--output", type=Path, default=ROOT / "benchmarks/results" /
                        datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ"))
    args = parser.parse_args()
    output = args.output.resolve()
    if not output.is_relative_to(ROOT):
        parser.error("Output must remain inside the project")
    try:
        run(json.loads(args.config.read_text()), output, args.model.expanduser().resolve())
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"Benchmark failed: {error}", file=sys.stderr)
        return 1
    print(f"Results: {output}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

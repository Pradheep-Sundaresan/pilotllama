import importlib.util
import csv
import io
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import tempfile
import threading
import unittest


MODULE = Path(__file__).resolve().parents[1] / "runners/concurrency_baseline.py"
spec = importlib.util.spec_from_file_location("concurrency_baseline", MODULE)
benchmark = importlib.util.module_from_spec(spec)
spec.loader.exec_module(benchmark)
WORKLOAD = MODULE.parents[1] / "workloads/concurrency.json"


class FixtureHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    lock = threading.Lock()
    next_id = 40
    records = {}

    def log_message(self, *_args):
        pass

    def send_json(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path == "/readyz":
            self.send_json(200, {"ready": True})
        elif self.path == "/v1/models":
            self.send_json(200, {"data": [{"id": "local-nemotron-4b"}]})
        elif self.path == "/internal/v1/scheduler":
            self.send_json(200, {"observed_at_unix_ms": 1, "source": "cpp_runtime_status_snapshot",
                                 "unit": "requests", "backend_state": "ready", "waiting_requests": 0,
                                 "executing_requests": 1, "queue_capacity": 2, "status_rpc_latency_ms": 0.1})
        elif self.path.startswith("/internal/v1/requests/"):
            record = self.records.get(self.path.rsplit("/", 1)[-1])
            self.send_json(200, record) if record else self.send_json(404, {"error": {"code": "request_not_found"}})
        else:
            self.send_json(404, {})

    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self.send_json(404, {})
            return
        size = int(self.headers.get("Content-Length", "0"))
        json.loads(self.rfile.read(size))
        with self.lock:
            type(self).next_id += 1
            request_id = str(type(self).next_id)
        events = [
            {"choices": [{"delta": {"role": "assistant", "content": ""}}]},
            {"choices": [{"delta": {"content": "hello"}}]},
            {"choices": [{"delta": {}, "finish_reason": "length"}]},
        ]
        body = b"".join(b"data: " + json.dumps(event).encode() + b"\n\n" for event in events) + b"data: [DONE]\n\n"
        record = {"request_id": request_id, "outcome": "completed", "handler_latency_ms": 2.0,
                  "native": {"output_tokens": 2, "queue_wait_ms": 0.5}}
        self.records[request_id] = record
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("X-Request-ID", request_id)
        self.end_headers()
        self.wfile.write(body)


class ConcurrencyBaselineTests(unittest.TestCase):
    def config(self):
        return json.loads(WORKLOAD.read_text())

    def test_config_and_localhost_bounds(self):
        config = self.config()
        benchmark.validate_config(config)
        self.assertEqual(benchmark.validate_base_url("http://127.0.0.1:8080/"), "http://127.0.0.1:8080")
        for update in ({"concurrency_levels": [1, 5]}, {"max_tokens": True},
                       {"prompt": "bad <|im_start|> prompt"}, {"repetitions_per_client": 0}):
            with self.subTest(update=update), self.assertRaises(ValueError):
                benchmark.validate_config(config | update)
        for url in ("https://127.0.0.1:8080", "http://localhost:8080", "http://127.0.0.1:8080/path"):
            with self.subTest(url=url), self.assertRaises(ValueError):
                benchmark.validate_base_url(url)
        config["runtime_declared"]["go_rate_per_minute"] = 0
        config["runtime_declared"]["go_rate_burst"] = 0
        benchmark.validate_config(config)

    def test_percentiles_require_enough_samples(self):
        small = benchmark.distribution([3, 1, 2])
        self.assertEqual(small, {"samples": 3, "p50": 2, "p95": None, "p99": None})
        twenty = benchmark.distribution(list(range(1, 21)))
        self.assertEqual(twenty["p95"], 19)
        self.assertIsNone(twenty["p99"])
        self.assertEqual(benchmark.distribution(list(range(1, 101)))["p99"], 99)

    def test_case_summary_uses_only_correlated_native_completions(self):
        case = {"case_duration_ms": 2000, "scheduler_failures": [],
                "scheduler_samples": [{"waiting_requests": 2, "executing_requests": 1},
                                      {"waiting_requests": 0, "executing_requests": 0}],
                "requests": [
                    {"outcome": "completed", "client_ttft_ms": 4.0, "client_latency_ms": 10.0,
                     "server_record": {"outcome": "completed", "handler_latency_ms": 9.0,
                                       "native": {"output_tokens": 8, "queue_wait_ms": 1.0}}},
                    {"outcome": "failed", "client_ttft_ms": None, "client_latency_ms": 2.0,
                     "server_record": None},
                ]}
        summary = benchmark.summarize_case(case)
        self.assertEqual(summary["completed_native_output_tokens"], 8)
        self.assertEqual(summary["completion_attributed_throughput_tokens_per_second"], 4)
        self.assertEqual(summary["sampled_max_waiting_requests"], 2)
        self.assertEqual(summary["sampled_busy_fraction"], 0.5)

    def test_model_free_http_run_writes_correlated_json_and_csv(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureHandler)
        serving = threading.Thread(target=server.serve_forever)
        serving.start()
        try:
            config = self.config() | {"concurrency_levels": [2], "repetitions_per_client": 1,
                                      "scheduler_poll_interval_ms": 20}
            with tempfile.TemporaryDirectory(dir=benchmark.ROOT / "benchmarks") as parent:
                output = Path(parent) / "result"
                result = benchmark.run(config, output, f"http://127.0.0.1:{server.server_port}",
                                       "fixture-token", "FIXTURE_TOKEN")
                self.assertEqual(result["status"], "complete")
                self.assertEqual(len(result["warmup"]["requests"]), 1)
                self.assertFalse(result["warmup"]["included_in_case_summaries"])
                self.assertEqual(result["cases"][0]["summary"]["server_records_matched"], 2)
                self.assertEqual(result["cases"][0]["summary"]["completed_native_output_tokens"], 4)
                self.assertEqual(len(result["cases"][0]["requests"][0]["content_sha256"]), 64)
                self.assertTrue((output / "results.json").is_file())
                csv_text = (output / "summary.csv").read_text()
                self.assertIn("client_ttft_p50_ms", csv_text)
                csv_row = next(csv.DictReader(io.StringIO(csv_text)))
                self.assertNotEqual(csv_row["client_ttft_p50_ms"], "")
                self.assertEqual(csv_row["requests"], "2")
                self.assertNotIn("fixture-token", (output / "results.json").read_text())
        finally:
            server.shutdown()
            server.server_close()
            serving.join()


if __name__ == "__main__":
    unittest.main()

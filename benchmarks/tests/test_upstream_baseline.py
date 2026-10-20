import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

MODULE = Path(__file__).resolve().parents[1] / "runners/upstream_baseline.py"
spec = importlib.util.spec_from_file_location("baseline", MODULE)
baseline = importlib.util.module_from_spec(spec)
spec.loader.exec_module(baseline)


class BaselineTests(unittest.TestCase):
    def row(self):
        return [{"n_prompt": 128, "n_gen": 0, "n_depth": 0, "build_commit": "abcdef",
                 "samples_ns": [1_000_000_000, 2_000_000_000], "samples_ts": [128.0, 64.0]}]

    def test_mean_is_mean_of_rates_not_rate_of_mean_time(self):
        result = baseline.summarize(self.row(), "prefill", 128, 2, "abcdef123")
        self.assertEqual(result["mean_tokens_per_second"], 96)
        self.assertEqual(result["mean_duration_ms"], 1500)
        self.assertAlmostEqual(result["stdev_tokens_per_second"], 45.254834)

    def test_reject_incomplete_wrong_workload_or_wrong_binary(self):
        for key, value in (("samples_ns", [1]), ("n_gen", 4), ("build_commit", "123456"),
                           ("samples_ts", [128.0, 999.0])):
            with self.subTest(key=key):
                raw = self.row()
                raw[0][key] = value
                with self.assertRaises(ValueError):
                    baseline.summarize(raw, "prefill", 128, 2, "abcdef123")

    def test_reject_invalid_workload_config(self):
        config = json.loads((MODULE.parents[1] / "workloads/baseline.json").read_text())
        baseline.validate_config(config)
        for key, value in (("repetitions", 1), ("threads", True), ("prompt_tokens", [0]),
                           ("decode_tokens", [32, 32]), ("microbatch_size", 256)):
            with self.subTest(key=key), self.assertRaises(ValueError):
                baseline.validate_config(config | {key: value})

    def test_memory_units_and_missing_value(self):
        self.assertEqual(baseline.peak_rss("  123456 maximum resident set size\n"), 123456)
        self.assertIsNone(baseline.peak_rss("measurement unavailable"))

    def test_decode_depth_does_not_inflate_timed_token_count(self):
        raw = self.row()
        raw[0].update(n_prompt=0, n_gen=128, n_depth=2048)
        result = baseline.summarize(raw, "decode", 128, 2, "abcdef123", depth=2048)
        self.assertEqual(result["mean_tokens_per_second"], 96)
        self.assertEqual(result["context_tokens_requested"], 2176)
        self.assertEqual(result["context_depth_tokens"], 2048)
        with self.assertRaises(ValueError):
            baseline.summarize(raw, "decode", 128, 2, "abcdef123", depth=512)

    def test_depth_cases_and_resource_bounds(self):
        config = json.loads((MODULE.parents[1] / "workloads/decode-depth.json").read_text())
        baseline.validate_config(config)
        self.assertEqual(baseline.cases(config), [("decode", 32, 0), ("decode", 32, 512),
                                                 ("decode", 32, 2048)])
        for update in ({"decode_depths": [-1]}, {"decode_depths": [True]},
                       {"decode_depths": [0, 0]}, {"decode_depths": [4096]},
                       {"decode_tokens": []}):
            with self.subTest(update=update), self.assertRaises(ValueError):
                baseline.validate_config(config | update)

    def test_failed_process_keeps_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            with self.assertRaises(RuntimeError):
                baseline.execute([sys.executable, "-c", "import sys; print('failure', file=sys.stderr); sys.exit(7)"], path, 5)
            self.assertIn("failure", (path / "stderr.log").read_text())

    def test_timeout_stops_process(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(subprocess.TimeoutExpired):
                baseline.execute([sys.executable, "-c", "import time; time.sleep(30)"], Path(directory), 0.05)


if __name__ == "__main__":
    unittest.main()

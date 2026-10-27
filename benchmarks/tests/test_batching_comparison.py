import importlib.util
from pathlib import Path
import unittest

PATH = Path(__file__).parents[1] / "runners" / "batching_comparison.py"
SPEC = importlib.util.spec_from_file_location("batching_comparison", PATH)
comparison = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(comparison)

class BatchingComparisonTests(unittest.TestCase):
    def test_speculative_totals_preserve_absence_and_measurements(self):
        absent = {"requests": [{"server_record": {"native": {"speculative_drafted_tokens": None}}}]}
        self.assertEqual(comparison.speculative_totals(absent), (None, None, None))
        measured = {"requests": [
            {"server_record": {"native": {"speculative_drafted_tokens": 8, "speculative_accepted_tokens": 2}}},
            {"server_record": {"native": {"speculative_drafted_tokens": 12, "speculative_accepted_tokens": 3}}},
        ]}
        self.assertEqual(comparison.speculative_totals(measured), (20, 5, 0.25))

if __name__ == "__main__": unittest.main()

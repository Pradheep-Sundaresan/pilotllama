import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest import mock

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

    def test_exact_qualification_matches_target_and_optional_draft(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target, draft, receipt = root / "target.gguf", root / "draft.gguf", root / "receipt"
            target.write_bytes(b"target"); draft.write_bytes(b"draft")
            target_fp = f"{target.stat().st_size}:{int(target.stat().st_mtime)}"
            draft_fp = f"{draft.stat().st_size}:{int(draft.stat().st_mtime)}"
            receipt.write_text(f"{target}\n{target_fp}\n{draft}\n{draft_fp}\n")
            with mock.patch.object(comparison, "QUALIFICATION", receipt):
                comparison.require_exact_qualification(target)
                comparison.require_exact_qualification(target, draft)
                receipt.write_text(f"{target}\n0:0\n")
                with self.assertRaisesRegex(ValueError, "target model"):
                    comparison.require_exact_qualification(target)

if __name__ == "__main__": unittest.main()

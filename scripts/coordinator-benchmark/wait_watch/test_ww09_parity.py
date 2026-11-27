"""Unit tests for WW09 mode-parity metrics extraction and comparison."""
from __future__ import annotations

import pathlib
import sys
import unittest

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wait_watch.suite_ww09_parity import compare_parity_runs, extract_parity_metrics  # noqa: E402


def _sample_bundle(run_id: str, *, presentations: int = 1, verdict: str = "pass") -> dict:
    return {
        "run_id": run_id,
        "checker_verdict": {"verdict": verdict, "defect_codes": [] if verdict == "pass" else ["D1"]},
        "observers": {
            "source": {
                "events": [{"seq": 1}, {"seq": 2}],
                "facts": {
                    "phase_records": [
                        {"phase": "process_report_published", "handle": "bg-1"},
                    ]
                },
            },
            "storage": {
                "counts": {
                    "watch_presentation_claims": 1,
                    "watch_deliveries": 1,
                    "watch_registrations": 1,
                    "wait_leases": 1,
                    "invocation_receipts": 2,
                },
                "tables": {"wait_leases": [{"status": "settled"}]},
            },
            "admission": {"events": [{"turn": 1}]},
            "rendered_input": {
                "new_model_presentation_count": presentations,
                "history_reread_count": 0,
                "delivery_receipt_count": 1,
            },
        },
    }


class TestWW09ModeParity(unittest.TestCase):
    def test_identical_off_and_on_runs_pass(self) -> None:
        off = _sample_bundle("run-off")
        on = _sample_bundle("run-on")
        res = compare_parity_runs(
            "c01-fast-exit",
            off,
            on,
            {"exists": False, "line_count": 0, "contradictions": 0, "overflows": 0, "parse_errors": 0},
            {"exists": False, "line_count": 0, "contradictions": 0, "overflows": 0, "parse_errors": 0},
        )
        self.assertTrue(res["passed"], res)
        self.assertEqual(res["mismatches"], [])
        self.assertEqual(extract_parity_metrics(off), extract_parity_metrics(on))

    def test_count_divergence_fails_parity(self) -> None:
        off = _sample_bundle("run-off", presentations=1)
        on = _sample_bundle("run-on", presentations=2)
        res = compare_parity_runs(
            "c01-parked-exit",
            off,
            on,
            {"exists": False, "line_count": 0, "contradictions": 0, "overflows": 0, "parse_errors": 0},
            {"exists": False, "line_count": 0, "contradictions": 0, "overflows": 0, "parse_errors": 0},
        )
        self.assertFalse(res["passed"])
        metrics = [m["metric"] for m in res["mismatches"]]
        self.assertIn("new_model_presentation_count", metrics)

    def test_disabled_writing_file_or_enabled_contradiction_fails(self) -> None:
        off = _sample_bundle("run-off")
        on = _sample_bundle("run-on")
        res = compare_parity_runs(
            "c01-fast-exit",
            off,
            on,
            {"exists": True, "line_count": 1, "contradictions": 0, "overflows": 0, "parse_errors": 0},
            {"exists": True, "line_count": 1, "contradictions": 1, "overflows": 0, "parse_errors": 0},
        )
        self.assertFalse(res["passed"])
        metrics = [m["metric"] for m in res["mismatches"]]
        self.assertIn("disabled_mode_wrote_diag_file", metrics)
        self.assertIn("enabled_diag_contradictions", metrics)


    def test_source_event_count_is_informational_only(self) -> None:
        off = _sample_bundle("run-off")
        on = _sample_bundle("run-on")
        on["observers"]["source"]["events"].append({"seq": 3, "kind": "process_exited"})
        res = compare_parity_runs(
            "c01-fast-exit",
            off,
            on,
            {"exists": False, "line_count": 0, "contradictions": 0, "overflows": 0, "parse_errors": 0},
            {"exists": False, "line_count": 0, "contradictions": 0, "overflows": 0, "parse_errors": 0},
        )
        self.assertTrue(res["passed"], res)
        self.assertEqual(res["mismatches"], [])
        self.assertEqual(res["disabled"]["informational"]["source_event_count"], 2)
        self.assertEqual(res["enabled"]["informational"]["source_event_count"], 3)


if __name__ == "__main__":
    unittest.main()

"""Unit tests qualifying the wait/watch deterministic checker over all 5 negative + 5 positive controls + unknown."""
from __future__ import annotations

import copy
import json
import pathlib
import sys
import tempfile
import unittest

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wait_watch.checker import (  # noqa: E402
    DEFECT_CODES,
    POSITIVE_CONTROL_NAMES,
    build_control_bundles,
    check_bundle,
    run_checker_controls,
)
from wait_watch import cli  # noqa: E402


class TestCheckerControls(unittest.TestCase):
    def test_all_eleven_controls_pass(self) -> None:
        report = run_checker_controls()
        self.assertTrue(report["all_passed"], json.dumps(report, indent=2))
        self.assertEqual(report["control_count"], 11)

    def test_five_negative_defects_detected(self) -> None:
        controls = build_control_bundles()
        for defect in DEFECT_CODES:
            with self.subTest(defect=defect):
                spec = controls[defect]
                got = check_bundle(spec["bundle"])
                self.assertEqual(got["verdict"], "fail")
                self.assertIn(defect, got["defect_codes"])

    def test_five_positive_controls_have_no_false_positives(self) -> None:
        controls = build_control_bundles()
        for name in POSITIVE_CONTROL_NAMES:
            with self.subTest(control=name):
                spec = controls[name]
                got = check_bundle(spec["bundle"])
                self.assertEqual(got["verdict"], "pass", json.dumps(got, indent=2))
                self.assertEqual(got["defect_codes"], [])

    def test_missing_instrumentation_yields_unknown_never_pass(self) -> None:
        controls = build_control_bundles()
        for missing_obs in ("source", "storage", "admission", "rendered_input"):
            with self.subTest(missing_obs=missing_obs):
                bundle = copy.deepcopy(controls["healthy_coalescing"]["bundle"])
                del bundle["observers"][missing_obs]
                got = check_bundle(bundle)
                self.assertEqual(got["verdict"], "unknown")
                self.assertIn(f"observer:{missing_obs}", got["missing_instrumentation"])

        # Explicit instrumentation_gaps also force unknown.
        bundle = copy.deepcopy(controls["ordinary_lag"]["bundle"])
        bundle["instrumentation_gaps"] = ["missing_sse_capture"]
        got = check_bundle(bundle)
        self.assertEqual(got["verdict"], "unknown")
        self.assertIn("missing_sse_capture", got["missing_instrumentation"])

    def test_in_process_lane_manual_clock_accepted(self) -> None:
        controls = build_control_bundles()
        bundle = copy.deepcopy(controls["ordinary_lag"]["bundle"])
        bundle["lane"] = "in-process"
        bundle["clock_domain"] = "manual"
        got = check_bundle(bundle)
        self.assertEqual(got["verdict"], "pass")
        self.assertEqual(got["diagnostics"]["lane"], "in-process")
        self.assertEqual(got["diagnostics"]["clock_domain"], "manual")

    def test_cli_controls_writes_report(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            out_dir = pathlib.Path(td) / "controls-out"
            rc = cli.main(["--controls", "--out", str(out_dir)])
            self.assertEqual(rc, 0)
            report_path = out_dir / "checker-controls.json"
            self.assertTrue(report_path.is_file())
            data = json.loads(report_path.read_text(encoding="utf-8"))
            self.assertTrue(data["all_passed"])
            self.assertEqual(data["control_count"], 11)


if __name__ == "__main__":
    unittest.main()

import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import ci_verification as ci
import verification_plan as planning


class HostedVerificationTests(unittest.TestCase):
    def test_check_partition_covers_every_local_gate_stage_once(self):
        lanes = ci.lanes()
        expected = sorted(stage["name"] for stage in planning.expand(["check"]))
        actual = sorted(stage["name"] for lane in lanes.values() if "check" in lane["profiles"]
                        for stage in planning.expand(lane["targets"]))
        self.assertEqual(actual, expected)
        check = {row["lane"] for row in ci.matrix("check")["include"]}
        release = {row["lane"] for row in ci.matrix("release")["include"]}
        # Releases gate on whether the product works; style, tooling, and the deep tiers run elsewhere.
        self.assertLessEqual(release, check)
        self.assertEqual(release, {"build", "contracts", "behavior", "frontend", "native", "vulnerabilities"})

    def test_partition_drift_refuses_to_plan_before_any_tests_run(self):
        for mutation in ("missing", "duplicate", "unknown", "unbounded"):
            with self.subTest(mutation=mutation):
                data = copy.deepcopy(planning.catalog())
                if mutation == "missing":
                    del data["ci"]["frontend"]
                elif mutation == "duplicate":
                    data["ci"]["duplicate"] = data["ci"]["frontend"]
                elif mutation == "unknown":
                    data["ci"]["frontend"]["targets"] = ["not:a:task"]
                else:
                    data["ci"]["frontend"]["minutes"] = 360
                with patch.object(ci, "catalog", return_value=data), self.assertRaises(ValueError):
                    ci.matrix("check")

    def test_nightly_selection_includes_only_requested_and_unconditional_jobs(self):
        lanes = ci.lanes()
        nightly = {target for row in ci.matrix("nightly")["include"] for target in lanes[row["lane"]]["targets"]}
        self.assertEqual(nightly, {"test:full", "test:race", "test:fuzz", "test:stress", "check:coverage",
                                   "den:coverage-check", "den:test:transcript-scale", "lint:vuln:fresh",
                                   "perf:bench", "perf:sidecar", "perf:soak"})
        for suite in ci.SUITES - {"all"}:
            with self.subTest(suite=suite):
                names = {row["lane"] for row in ci.matrix("nightly", suite)["include"]}
                expected = {name for name, lane in lanes.items() if "nightly" in lane["profiles"]
                            and lane["suite"] in {"all", suite}}
                self.assertEqual(names, expected)
                self.assertIn("vulnerability-freshness", names)
        for profile, suite in [("unknown", "all"), ("release", "race"), ("nightly", "typo")]:
            with self.assertRaises(ValueError):
                ci.matrix(profile, suite)

    def test_host_platform_and_time_budgets_are_explicit(self):
        for profile in ci.PROFILES:
            for row in ci.matrix(profile)["include"]:
                with self.subTest(profile=profile, lane=row["lane"]):
                    self.assertEqual(row["job_minutes"] - row["minutes"], 30)
                    self.assertLess(row["job_minutes"], 360)
                    if profile != "check" or row["lane"] == "webkit":
                        self.assertEqual(row["runner"], "macos-15")

    def test_aggregate_rejects_failure_cancellation_skip_and_missing_results(self):
        ci.require_success({"a": {"result": "success"}, "b": {"result": "success"}})
        for status in ["failure", "cancelled", "skipped", "", None]:
            with self.subTest(status=status), self.assertRaises(ValueError):
                ci.require_success({"a": {"result": "success"}, "b": {"result": status}})
        for results in [{}, [], {"a": {}}]:
            with self.assertRaises(ValueError):
                ci.require_success(results)

    def test_lane_uses_task_admission_and_preserves_the_verdict(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for code, status in [(0, "passed"), (1, "failed"), (2, "unverified"), (-15, "unverified")]:
                with patch.object(ci, "artifact_root", return_value=root), \
                        patch.object(ci.subprocess, "call", return_value=code) as run:
                    self.assertEqual(ci.run_lane("frontend"), code)
                    run.assert_called_once()
                    self.assertEqual(run.call_args.args[0], ["./task", "den:typecheck", "den:lint", "den:test"])
                    self.assertEqual(run.call_args.kwargs["cwd"], ci.ROOT)
                record = json.loads((root / "ci/run.json").read_text())
                self.assertEqual(record["status"], status)
                self.assertGreaterEqual(record["finished_at"], record["started_at"])

    def test_lane_budget_bounds_the_go_watchdog_unless_the_caller_sets_one(self):
        minutes = ci.lanes()["behavior"]["minutes"]
        with tempfile.TemporaryDirectory() as directory:
            for inherited, expected in [({}, str(minutes * 60)), ({"PW_GO_TEST_TIMEOUT_SECONDS": "60"}, "60")]:
                with patch.object(ci, "artifact_root", return_value=Path(directory)), \
                        patch.dict(ci.os.environ, inherited), \
                        patch.object(ci.subprocess, "call", return_value=0) as run:
                    if not inherited:
                        ci.os.environ.pop("PW_GO_TEST_TIMEOUT_SECONDS", None)
                    ci.run_lane("behavior")
                    self.assertEqual(run.call_args.kwargs["env"]["PW_GO_TEST_TIMEOUT_SECONDS"], expected)

    def test_interrupted_invocation_leaves_an_unfinished_record(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with patch.object(ci, "artifact_root", return_value=root), \
                    patch.object(ci.subprocess, "call", side_effect=KeyboardInterrupt), \
                    self.assertRaises(KeyboardInterrupt):
                ci.run_lane("race")
            record = json.loads((root / "ci/run.json").read_text())
            self.assertEqual(record["status"], "running")
            self.assertNotIn("exit_code", record)

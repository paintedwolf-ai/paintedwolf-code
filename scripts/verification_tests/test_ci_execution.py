import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import ci_verification as ci
import verification_plan as planning


class HostedVerificationTests(unittest.TestCase):
    def test_tier_partitions_cover_every_local_gate_stage_once(self):
        lanes = ci.lanes()
        self.assertEqual(ci.GATES, {"fast": "check-fast", "check": "check"})
        for profile, gate in ci.GATES.items():
            with self.subTest(profile=profile):
                expected = sorted(stage["name"] for stage in planning.expand([gate]))
                actual = sorted(stage["name"] for lane in lanes.values() if profile in lane["profiles"]
                                for stage in planning.expand(lane["targets"]))
                self.assertEqual(actual, expected)
        check = {row["lane"] for row in ci.matrix("check")["include"]}
        release = {row["lane"] for row in ci.matrix("release")["include"]}
        # Releases gate on whether the product works; style, tooling, and the deep tiers run elsewhere.
        self.assertLessEqual(release, check)
        self.assertEqual(release, {"build", "contracts", "behavior", "frontend", "native", "vulnerabilities"})

    def test_partition_drift_refuses_to_plan_before_any_tests_run(self):
        for mutation in ("missing", "fast-missing", "duplicate", "unknown", "unbounded"):
            with self.subTest(mutation=mutation):
                data = copy.deepcopy(planning.catalog())
                if mutation == "missing":
                    del data["ci"]["frontend"]
                elif mutation == "fast-missing":
                    data["ci"]["fast-go"]["targets"].remove("test:contract")
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
                    # macOS hosts only the lanes that test macOS-specific behavior.
                    self.assertEqual(row["runner"], "macos-15" if row["lane"] == "webkit" else "ubuntu-latest")

    def test_aggregate_rejects_failure_cancellation_skip_and_missing_results(self):
        ci.require_success({"a": {"result": "success"}, "b": {"result": "success"}})
        for status in ["failure", "cancelled", "skipped", "", None]:
            with self.subTest(status=status), self.assertRaises(ValueError):
                ci.require_success({"a": {"result": "success"}, "b": {"result": status}})
        for results in [{}, [], {"a": {}}]:
            with self.assertRaises(ValueError):
                ci.require_success(results)

    def test_aggregate_accepts_only_the_skips_a_tier_declares(self):
        ci.require_success({"verification": {"result": "success"}, "platform": {"result": "skipped"}}, ["platform"])
        for platform, skipped in [("success", ["platform"]), ("failure", ["platform"]), ("skipped", [])]:
            with self.subTest(platform=platform, skipped=skipped), self.assertRaises(ValueError):
                ci.require_success({"verification": {"result": "success"}, "platform": {"result": platform}}, skipped)
        # Declaring a skip never excuses the jobs the tier does run.
        with self.assertRaises(ValueError):
            ci.require_success({"verification": {"result": "skipped"}, "platform": {"result": "skipped"}}, ["platform"])
        with self.assertRaises(ValueError):
            ci.require_success({"verification": {"result": "success"}}, ["platform"])

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

    def test_lane_worker_cap_reaches_admission(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(ci, "artifact_root", return_value=Path(directory)), \
                patch.object(ci.subprocess, "call", return_value=0) as run:
            ci.run_lane("behavior")
            self.assertEqual(run.call_args.kwargs["env"]["PW_TEST_WORKERS"], str(ci.lanes()["behavior"]["workers"]))
            ci.run_lane("frontend")
            self.assertEqual(run.call_args.kwargs["env"].get("PW_TEST_WORKERS"), ci.os.environ.get("PW_TEST_WORKERS"))

    @unittest.skipUnless(Path("/proc/meminfo").exists(), "reads Linux /proc")
    def test_resource_line_reports_memory_disk_and_largest_processes(self):
        line = ci.resource_line()
        self.assertRegex(line, r"available \d+ MiB, swap free \d+ MiB; disk free / \d+ GiB")
        self.assertRegex(line, r"largest \S+\[\d+\] \d+ MiB")

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

    def test_release_commit_needs_a_passing_full_tier_check(self):
        def fake(runs, events):
            def github(path, **query):
                if path.endswith("/check-runs"):
                    self.assertEqual(query, {"check_name": "check", "filter": "all"})
                    return {"check_runs": [{"app": {"slug": slug}, "conclusion": conclusion, "check_suite": {"id": suite}}
                                           for slug, conclusion, suite in runs]}
                return {"workflow_runs": [{"event": events[query["check_suite_id"]]}]}
            return github
        actions = "github-actions"
        for runs, events in [([(actions, "success", 1)], {1: "merge_group"}),
                             ([(actions, "failure", 1), (actions, "success", 2)], {1: "merge_group", 2: "workflow_dispatch"})]:
            with patch.object(ci, "github", fake(runs, events)):
                ci.require_full_tier("owner/repo", "abc")
        for runs, events in [([], {}),
                             ([(actions, "success", 1)], {1: "pull_request"}),
                             ([(actions, "success", 1)], {1: "push"}),
                             ([(actions, "failure", 1)], {1: "merge_group"}),
                             ([("impostor", "success", 1)], {1: "merge_group"})]:
            with self.subTest(runs=runs, events=events), patch.object(ci, "github", fake(runs, events)), \
                    self.assertRaises(ValueError):
                ci.require_full_tier("owner/repo", "abc")

    def test_report_names_what_did_not_pass_in_the_summary_and_annotations(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            batch = root / "verification" / "batch"
            (batch / "stage-001-failures").mkdir(parents=True)
            output = batch / "stage-001-failures" / "pkg.log"
            output.write_text("=== RUN   TestBudget\n    budget_test.go:31: over cap\n")
            log = batch / "stage-002.log"
            log.write_text("\n".join(f"line {index}" for index in range(ci.EXCERPT_LINES + 10)) + "\n")
            receipts = {
                "1-go.json": {"status": "failed", "names": ["test:full"], "evidence": [{"stage": "test:full", "results": {
                    "example/pkg": {"status": "failed", "tests": ["TestBudget"], "output": str(output)},
                    "example/other": {"status": "passed"}}}]},
                "2-task.json": {"status": "failed", "names": ["lint:full"], "evidence": [{"stage": "lint:full", "results": {
                    "task": {"status": "failed", "log": str(log)}}}]},
                "3-stopped.json": {"status": "unverified", "names": ["den:test"], "evidence": [],
                                   "reason": "cancelled before admission"},
                "4-passed.json": {"status": "passed", "names": ["build"], "evidence": []},
            }
            for name, receipt in receipts.items():
                (batch / name).write_text(json.dumps(receipt))
            (batch / "1-go.progress.json").write_text("{}")
            summary = root / "summary.md"
            with patch.object(ci, "artifact_root", return_value=root), \
                    patch.dict(ci.os.environ, {"GITHUB_STEP_SUMMARY": str(summary)}), \
                    patch("builtins.print") as printed:
                ci.report("failure")
            text = summary.read_text()
            self.assertIn("- **failed** `test:full` · `example/pkg`: `TestBudget`", text)
            self.assertIn("budget_test.go:31: over cap", text)
            self.assertNotIn("example/other", text)
            self.assertIn("- **failed** `lint:full`", text)
            self.assertIn(f"line {ci.EXCERPT_LINES + 9}", text)
            self.assertNotIn("line 9\n", text)
            self.assertIn("- **unverified** `den:test` (cancelled before admission)", text)
            self.assertNotIn("`build`", text)
            annotations = [call.args[0].splitlines()[0] for call in printed.call_args_list]
            self.assertEqual(annotations, [
                "::error title=test:full failed::example/pkg: TestBudget",
                "::error title=lint:full failed::see the job summary",
                "::error title=den:test unverified::cancelled before admission",
            ])

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

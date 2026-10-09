import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import ci_verification as ci
import verification_plan as planning
from verification_execute import shard_packages


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
                                   "perf:bench", "perf:sidecar", "perf:soak", "den:webkit:scroll"})
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

    def test_each_lane_installs_only_its_declared_setup(self):
        setups = {row["lane"]: row["setup"] for row in ci.matrix("check")["include"]}
        self.assertEqual(setups["native"], "shell")
        # The WebKit harness builds without the shell, so the lane skips its packaging inputs.
        nightly = {row["lane"]: row["setup"] for row in ci.matrix("nightly", "e2e")["include"]}
        self.assertEqual(nightly["webkit"], "harness")
        self.assertEqual({setup for lane, setup in setups.items() if lane not in {"native", "webkit"}},
                         {"verification"})
        for mutation in ("unknown", "missing", "boolean"):
            with self.subTest(mutation=mutation):
                data = copy.deepcopy(planning.catalog())
                lane = data["ci"]["webkit"]
                if mutation == "unknown":
                    lane["setup"] = "everything"
                elif mutation == "missing":
                    del lane["setup"]
                else:
                    del lane["setup"]
                    lane["native"] = True
                with patch.object(ci, "catalog", return_value=data), self.assertRaises(ValueError):
                    ci.matrix("check")

    def test_e2e_leaves_merge_admission_and_has_an_explicit_nightly_selection(self):
        self.assertNotIn("den:webkit:scroll", [stage["name"] for stage in planning.expand(["check"])])
        self.assertNotIn("webkit", {row["lane"] for row in ci.matrix("check")["include"]})
        self.assertEqual({row["lane"] for row in ci.matrix("nightly", "e2e")["include"]},
                         {"webkit", "vulnerability-freshness"})

    def test_behavior_shards_preserve_the_full_recipe_and_memory_cap(self):
        lane = ci.lanes()["behavior"]
        for profile in ["check", "nightly", "release"]:
            rows = [row for row in ci.matrix(profile)["include"] if row["lane"] == "behavior"]
            self.assertEqual([row["shard"] for row in rows], ["1/2", "2/2"])
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(ci, "artifact_root", return_value=Path(directory)), \
                patch.object(ci.subprocess, "call", return_value=0) as run:
            for shard in ["1/2", "2/2"]:
                ci.run_lane("behavior", shard)
                self.assertEqual(run.call_args.args[0], ["./task", "test:full"])
                environment = run.call_args.kwargs["env"]
                self.assertEqual(environment["PW_GO_SHARD"], shard)
                self.assertEqual(environment["PW_TEST_WORKERS"], str(lane["workers"]))
            with self.assertRaises(ValueError):
                ci.run_lane("behavior")

    def test_combined_analysis_targets_restore_both_tool_sets(self):
        self.assertEqual(ci.analysis_set(["lint:full", "lint:vuln"]), "all")

    def test_lane_setup_restores_only_the_required_tool_sets(self):
        for profile in ci.PROFILES:
            for row in ci.matrix(profile)["include"]:
                targets = ci.lanes()[row["lane"]]["targets"]
                self.assertEqual(row["notices"], "licenses:notices" in targets)
                if row["analysis"] == "lint":
                    self.assertTrue(set(targets) & {"lint:fast", "lint:full"})
                elif row["analysis"] == "vulnerabilities":
                    self.assertTrue(set(targets) & {"lint:vuln", "lint:vuln:fresh"})
                else:
                    self.assertEqual(row["analysis"], "none")
        for profile in ["fast", "check"]:
            jobs = [row for row in ci.matrix(profile)["include"] if row["notices"]]
            self.assertEqual(len(jobs), 1)

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

    def test_draft_pull_requests_never_pass_the_required_check(self):
        # Drafts skip verification, and a skipped required check would otherwise read as passing.
        for results in [{"verification": {"result": "skipped"}, "platform": {"result": "skipped"}},
                        {"verification": {"result": "success"}, "platform": {"result": "skipped"}}]:
            with self.subTest(results=results), self.assertRaisesRegex(ValueError, "ready for review"):
                ci.require_success(results, ["platform"], draft=True)

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
            ci.run_lane("behavior", "1/2")
            self.assertEqual(run.call_args.kwargs["env"]["PW_TEST_WORKERS"], str(ci.lanes()["behavior"]["workers"]))
            ci.run_lane("frontend")
            self.assertEqual(run.call_args.kwargs["env"].get("PW_TEST_WORKERS"), ci.os.environ.get("PW_TEST_WORKERS"))

    @unittest.skipUnless(Path("/proc/meminfo").exists(), "reads Linux /proc")
    def test_resource_line_reports_memory_disk_and_largest_processes(self):
        line = ci.resource_line()
        self.assertRegex(line, r"available \d+ MiB, swap free \d+ MiB; disk free / \d+ GiB")
        self.assertRegex(line, r"largest \S+\[\d+\] \d+ MiB")

    def test_sharded_lane_expands_into_jobs_that_each_select_their_slice(self):
        count = ci.lanes()["race"]["shards"]
        rows = [row for row in ci.matrix("nightly", "race")["include"] if row["lane"] == "race"]
        self.assertEqual([row["shard"] for row in rows], [f"{k}/{count}" for k in range(1, count + 1)])
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(ci, "artifact_root", return_value=Path(directory)), \
                patch.object(ci.subprocess, "call", return_value=0) as run:
            ci.run_lane("race", f"2/{count}")
            self.assertEqual(run.call_args.kwargs["env"]["PW_GO_SHARD"], f"2/{count}")
            for lane, shard in [("race", ""), ("race", f"{count + 1}/{count}"), ("frontend", "1/2")]:
                with self.subTest(lane=lane, shard=shard), self.assertRaises(ValueError):
                    ci.run_lane(lane, shard)

    def test_shards_partition_the_package_selection(self):
        packages = [f"p{n:02d}" for n in range(10)]
        slices = [shard_packages(packages, f"{k}/3") for k in range(1, 4)]
        self.assertEqual(sorted(p for part in slices for p in part), packages)
        self.assertEqual(slices[0], ["p00", "p03", "p06", "p09"])
        self.assertEqual(shard_packages(packages, ""), packages)
        for shard in ["0/3", "4/3", "x/3", "3", "2/1"]:
            with self.subTest(shard=shard), self.assertRaises(ValueError):
                shard_packages(packages, shard)
        with self.assertRaises(ValueError):
            shard_packages(["only"], "2/2")

    def test_lane_budget_bounds_the_go_watchdog_unless_the_caller_sets_one(self):
        minutes = ci.lanes()["behavior"]["minutes"]
        with tempfile.TemporaryDirectory() as directory:
            for inherited, expected in [({}, str(minutes * 60)), ({"PW_GO_TEST_TIMEOUT_SECONDS": "60"}, "60")]:
                with patch.object(ci, "artifact_root", return_value=Path(directory)), \
                        patch.dict(ci.os.environ, inherited), \
                        patch.object(ci.subprocess, "call", return_value=0) as run:
                    if not inherited:
                        ci.os.environ.pop("PW_GO_TEST_TIMEOUT_SECONDS", None)
                    ci.run_lane("behavior", "1/2")
                    self.assertEqual(run.call_args.kwargs["env"]["PW_GO_TEST_TIMEOUT_SECONDS"], expected)

    def test_release_commit_needs_exact_main_qualification(self):
        good = {"head_sha": "abc", "head_branch": "main", "conclusion": "success", "event": "push"}
        with patch.object(ci, "github", return_value={"workflow_runs": [good]}):
            ci.require_full_tier("owner/repo", "abc")
        for changed in ({"head_sha": "other"}, {"head_branch": "feature"}, {"conclusion": "failure"},
                        {"event": "merge_group"}, {"event": "pull_request"}):
            with patch.object(ci, "github", return_value={"workflow_runs": [{**good, **changed}]}), self.assertRaises(ValueError):
                ci.require_full_tier("owner/repo", "abc")

    def test_prune_cancels_only_runs_whose_merge_group_is_gone(self):
        live, gone = "gh-readonly-queue/main/pr-2-b", "gh-readonly-queue/main/pr-1-a"
        calls = []

        def github(path, method="GET", **query):
            calls.append((method, path, query.get("status")))
            if path.endswith("/runs"):
                self.assertEqual((query["event"], query["per_page"]), ("merge_group", 100))
                runs = {"queued": [{"id": 1, "head_branch": gone}], "in_progress": [{"id": 2, "head_branch": live}]}
                return {"workflow_runs": runs.get(query["status"], [])}
            if "matching-refs" in path:
                return [{"ref": f"refs/heads/{live}"}]
            return None

        with patch.object(ci, "github", github), patch("builtins.print"):
            self.assertEqual(ci.prune_merge_queue("owner/repo"), [1])
        self.assertEqual([call for call in calls if call[0] == "POST"],
                         [("POST", "repos/owner/repo/actions/runs/1/force-cancel", None)])
        # Runs are listed before branches, so a group created in between is treated as live.
        listed = [index for index, call in enumerate(calls) if call[1].endswith("/runs")]
        branches = next(index for index, call in enumerate(calls) if "matching-refs" in call[1])
        self.assertLess(max(listed), branches)
        self.assertEqual({call[2] for call in calls if call[1].endswith("/runs")}, set(ci.UNFINISHED_RUNS))

    def test_report_names_what_did_not_pass_in_the_summary_and_annotations(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            batch = root / "verification" / "batch"
            (batch / "stage-001-failures").mkdir(parents=True)
            output = batch / "stage-001-failures" / "pkg.log"
            output.write_text("=== RUN   TestOther\n=== PAUSE TestOther\n--- PASS: TestOther (0.01s)\n"
                              "=== RUN   TestBudget\n    budget_test.go:31: over cap\n--- FAIL: TestBudget (0.02s)\n")
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
            self.assertIn("--- FAIL: TestBudget", text)
            self.assertNotIn("TestOther", text)
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
                ci.run_lane("stress")
            record = json.loads((root / "ci/run.json").read_text())
            self.assertEqual(record["status"], "running")
            self.assertNotIn("exit_code", record)

    def test_release_withdraws_every_leftover_request_through_task_cancel(self):
        status = {"runs": [
            {"kind": "request", "name": "e2e:den", "ticket": "1-a", "state": "sharing"},
            {"kind": "batch", "name": "verification batch", "ticket": "1-a-batch", "members": ["1-a"]},
            {"kind": "request", "name": "e2e:cleanup", "ticket": "2-b", "state": "queued"},
        ]}
        calls = []

        def task_json(arguments):
            calls.append(arguments)
            if arguments == ["test:status"]:
                return 0, status
            return (0, {"released": True}) if arguments[2] == "1-a" else (2, {"released": False})

        with patch.object(ci, "task_json", task_json), patch.dict(ci.os.environ, {"GITHUB_ACTIONS": "true"}), \
                patch("builtins.print"):
            self.assertEqual(ci.release_leftover_requests("Playwright web E2E"), ["2-b"])
        # Batches release once their members are withdrawn; only requests are cancelled.
        self.assertEqual(calls[1:], [
            ["test:cancel", "--", "1-a", "--force", "--reason", "e2e:den outlived the Playwright web E2E step"],
            ["test:cancel", "--", "2-b", "--force", "--reason", "e2e:cleanup outlived the Playwright web E2E step"],
        ])

    def test_release_refuses_outside_a_hosted_runner(self):
        with patch.dict(ci.os.environ, {"GITHUB_ACTIONS": ""}), patch.object(ci, "task_json") as task, \
                self.assertRaises(ValueError):
            ci.release_leftover_requests("cleanup")
        task.assert_not_called()

    def test_task_json_reads_past_the_command_echo(self):
        result = ci.subprocess.CompletedProcess([], 2, stdout='task: [test:cancel] python3 x\n{"released": false}\n')
        with patch.object(ci.subprocess, "run", return_value=result) as run:
            self.assertEqual(ci.task_json(["test:cancel"]), (2, {"released": False}))
        self.assertEqual(run.call_args.args[0], ["./task", "test:cancel"])
        refused = ci.subprocess.CompletedProcess([], 1, stdout="")
        with patch.object(ci.subprocess, "run", return_value=refused):
            self.assertEqual(ci.task_json(["test:cancel"]), (1, None))

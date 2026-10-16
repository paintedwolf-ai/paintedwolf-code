import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import hosted_execution as ci
from ci_policy import hosted_plan
from verification_execute import shard_packages


class HostedExecutionTests(unittest.TestCase):
    def test_behavior_shards_preserve_the_full_recipe_and_memory_cap(self):
        lane = hosted_plan.lanes()["behavior"]
        for profile in ["check", "nightly", "release"]:
            rows = [row for row in hosted_plan.matrix(profile)["include"] if row["lane"] == "behavior"]
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
            self.assertEqual(run.call_args.kwargs["env"]["PW_TEST_WORKERS"], str(hosted_plan.lanes()["behavior"]["workers"]))
            ci.run_lane("frontend")
            self.assertEqual(run.call_args.kwargs["env"].get("PW_TEST_WORKERS"), ci.os.environ.get("PW_TEST_WORKERS"))

    @unittest.skipUnless(Path("/proc/meminfo").exists(), "reads Linux /proc")
    def test_resource_line_reports_memory_disk_and_largest_processes(self):
        line = ci.resource_line()
        self.assertRegex(line, r"available \d+ MiB, swap free \d+ MiB; disk free / \d+ GiB")
        self.assertRegex(line, r"largest .+\[\d+\] \d+ MiB")

    def test_resource_line_preserves_process_names_with_spaces(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            proc = root / "proc"
            process = proc / "123"
            process.mkdir(parents=True)
            (proc / "meminfo").write_text("MemAvailable: 2048 kB\nSwapFree: 1024 kB\n")
            (process / "status").write_text("Name:\tIsolated Web Co\nVmRSS:\t3072 kB\n")
            with patch.object(ci, "Path", side_effect=lambda path: root / path.lstrip("/")):
                line = ci.resource_line()
            self.assertIn("available 2 MiB, swap free 1 MiB", line)
            self.assertIn("largest Isolated Web Co[123] 3 MiB", line)

    def test_sharded_lane_expands_into_jobs_that_each_select_their_slice(self):
        count = hosted_plan.lanes()["race"]["shards"]
        rows = [row for row in hosted_plan.matrix("nightly", "race")["include"] if row["lane"] == "race"]
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
        # A selection smaller than the split repeats packages rather than leave a shard empty.
        self.assertEqual([shard_packages(["a", "b"], f"{k}/3") for k in range(1, 4)], [["a"], ["b"], ["a"]])

    def test_measured_shards_finish_together(self):
        durations = {"security": 640, "git": 270, "wiring": 260, "release": 200, "session": 150,
                     "hostcontracts": 166, "sourcecontracts": 110, "small": 1, "tiny": 0}
        packages = sorted([*durations, "unmeasured"])
        slices = [shard_packages(packages, f"{k}/2", durations) for k in range(1, 3)]
        self.assertEqual(sorted(p for part in slices for p in part), packages)
        loads = [sum(durations.get(p, 166) for p in part) for part in slices]
        self.assertLessEqual(max(loads) - min(loads), 200)
        self.assertIn("security", slices[0])
        # Every shard of a run computes the same partition from the same measurements.
        self.assertEqual(slices, [shard_packages(list(reversed(packages)), f"{k}/2", dict(durations)) for k in range(1, 3)])

    def test_unmeasured_shards_deal_the_sorted_selection_in_turn(self):
        packages = [f"p{n:02d}" for n in range(7)]
        for durations in (None, {}, {"other": 5}):
            with self.subTest(durations=durations):
                self.assertEqual([shard_packages(packages, f"{k}/3", durations) for k in range(1, 4)],
                                 [packages[k::3] for k in range(3)])

    def test_shards_receive_the_plans_measurements_and_tasks_do_not(self):
        measured = '{"github.com/lycaon/lycaon/test/security": 640}'
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(ci, "artifact_root", return_value=Path(directory)), \
                patch.dict(ci.os.environ, {"PW_CI_PACKAGE_TIMINGS": measured}), \
                patch.object(ci.subprocess, "call", return_value=0) as run:
            ci.run_lane("behavior", "2/2")
            environment = run.call_args.kwargs["env"]
            self.assertNotIn("PW_CI_PACKAGE_TIMINGS", environment)
            self.assertEqual(Path(environment["PW_GO_TIMINGS"]).read_text(), measured)
            ci.run_lane("frontend")
            environment = run.call_args.kwargs["env"]
            self.assertNotIn("PW_CI_PACKAGE_TIMINGS", environment)
            self.assertNotIn("PW_GO_TIMINGS", environment)

    def test_affected_behavior_shards_deal_the_closure_in_admission(self):
        scope = {"base": "b" * 40, "head": "a" * 40, "paths": ["lycaon/internal/a/a.go"], "full": False, "reasons": []}
        with tempfile.TemporaryDirectory() as directory, \
                patch.dict(ci.os.environ, {"PW_CI_AFFECTED": "1"}), \
                patch("ci_policy.impact.change", return_value=scope), \
                patch("ci_policy.impact.go_scope", return_value=["./internal/a", "./test/contract"]), \
                patch.object(ci, "artifact_root", return_value=Path(directory)), \
                patch.object(ci.subprocess, "call", return_value=0) as run:
            ci.run_lane("behavior", "1/2")
        self.assertEqual(run.call_args.args[0], ["./task", "test:full", "--", "./internal/a", "./test/contract"])
        self.assertEqual(run.call_args.kwargs["env"]["PW_GO_SHARD"], "1/2")

    def test_lane_budget_bounds_the_go_watchdog_unless_the_caller_sets_one(self):
        minutes = hosted_plan.lanes()["behavior"]["minutes"]
        with tempfile.TemporaryDirectory() as directory:
            for inherited, expected in [({}, str(minutes * 60)), ({"PW_GO_TEST_TIMEOUT_SECONDS": "60"}, "60")]:
                with patch.object(ci, "artifact_root", return_value=Path(directory)), \
                        patch.dict(ci.os.environ, inherited), \
                        patch.object(ci.subprocess, "call", return_value=0) as run:
                    if not inherited:
                        ci.os.environ.pop("PW_GO_TEST_TIMEOUT_SECONDS", None)
                    ci.run_lane("behavior", "1/2")
                    self.assertEqual(run.call_args.kwargs["env"]["PW_GO_TEST_TIMEOUT_SECONDS"], expected)

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

    def test_full_qualification_budgets_receive_push_change_base(self):
        scope = dict(base='b' * 40, head='a' * 40, paths=['lycaon/internal/a/a.go'], full=True, reasons=[])
        with tempfile.TemporaryDirectory() as directory, \
                patch.dict('os.environ', PW_CI_AFFECTED='0'), \
                patch('ci_policy.impact.change', return_value=scope), \
                patch.object(ci, 'artifact_root', return_value=Path(directory)), \
                patch.object(ci.subprocess, 'call', return_value=0) as command:
            ci.run_lane('limits')
        self.assertEqual(command.call_args.kwargs['env']['PW_CHANGE_BASE'], 'b' * 40)

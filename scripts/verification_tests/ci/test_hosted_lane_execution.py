import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import verification_plan
from ci_policy import hosted_execution as ci
from ci_policy import hosted_plan
from verification_execute import shard_packages
import verification_execute as execution
from verification_tests.ci.fixture_plan import live

BEHAVIOR = live(phase="affected-packages")
ADMISSION, ADMISSION_TESTS = live(phase="admission"), live(phase="admission-tests")
DEN, RACE, CONFINE = live(runs="den:typecheck"), live(runs="test:race", profiles=["nightly"]), live(runs="test:confine:linux")
NIX, BUILD = live(runs="nix:check"), live(runs="check:drift")
SCRIPTS = live(runs="scripts:coverage:changes")
STRESS = next(name for name, lane in hosted_plan.lanes().items()
              if lane["targets"] == ["test:stress"] and lane["profiles"] == ["nightly"])


class HostedExecutionTests(unittest.TestCase):
    def setUp(self):
        scope = patch('ci_policy.impact.go_scope', return_value=None)
        scope.start()
        self.addCleanup(scope.stop)

    def test_setup_timing_requires_an_observed_job_start(self):
        for marker, basis in [('1', 'setup-and-execution'), ('', 'execution'), ('invalid', 'execution')]:
            with tempfile.TemporaryDirectory() as directory, \
                    patch.dict(ci.os.environ, {'PW_CI_JOB_STARTED_AT': marker}), \
                    patch.object(ci, 'artifact_root', return_value=Path(directory)), \
                    patch.object(ci.subprocess, 'call', return_value=0):
                ci.run_lane(DEN)
                timing = json.loads((Path(directory) / 'ci/timings.json').read_text())
                self.assertEqual(timing['basis'], basis)

    def test_behavior_shards_preserve_the_full_recipe_and_memory_cap(self):
        lane = hosted_plan.lanes()[BEHAVIOR]
        for profile in ["check", "nightly", "release"]:
            rows = [row for row in hosted_plan.matrix(profile)["include"] if row["lane"] == BEHAVIOR]
            self.assertEqual([row["shard"] for row in rows], ["1/3", "2/3", "3/3"])
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(ci, "artifact_root", return_value=Path(directory)), \
                patch.object(ci.subprocess, "call", return_value=0) as run:
            for shard in ["1/3", "2/3", "3/3"]:
                ci.run_lane(BEHAVIOR, shard)
                self.assertEqual(run.call_args.args[0], ["./task", "test:full"])
                environment = run.call_args.kwargs["env"]
                self.assertEqual(environment["PW_GO_SHARD"], shard)
                self.assertEqual(environment["PW_TEST_WORKERS"], str(lane["workers"]))
            with self.assertRaises(ValueError):
                ci.run_lane(BEHAVIOR)

    def test_captured_direct_execution_partitions_the_actual_go_packages(self):
        stage = {"kind": "go", "packages": ["./..."], "exclude": "", "options": [], "flags": []}
        records = "".join(json.dumps({"ImportPath": name}) for name in ["module/a", "module/b", "module/c", "module/d"])
        partitions = []
        for shard in ["1/2", "2/2"]:
            with patch.dict(execution.os.environ, {"PW_GO_SHARD": shard, "PW_SOURCE_SNAPSHOT_COMMIT": "a" * 40}), \
                    patch.object(execution, "expand", return_value=[stage]), \
                    patch.object(execution.subprocess, "run", return_value=type("Result", (), {"stdout": records})()), \
                    patch.object(execution, "digest_command", return_value=["digest"]) as digest, \
                    patch.object(execution, "run_direct", return_value=0):
                self.assertEqual(execution.direct(["test:full"], []), 0)
                partitions.append(set(digest.call_args.args[1]))
        self.assertFalse(partitions[0] & partitions[1])
        self.assertEqual(partitions[0] | partitions[1], {"module/a", "module/b", "module/c", "module/d"})

    def test_direct_empty_share_never_expands_into_default_go_selection(self):
        stage = {"kind": "go", "packages": ["./..."], "exclude": "", "options": [], "flags": []}
        with patch.dict(execution.os.environ, {"PW_GO_SHARD": "2/2"}), \
                patch.object(execution, "expand", return_value=[stage]), \
                patch.object(execution.subprocess, "run", return_value=type("Result", (), {"stdout": '{"ImportPath":"module/a"}'})()), \
                patch.object(execution, "run_direct") as run:
            self.assertEqual(execution.direct(["test:full"], []), 0)
            run.assert_not_called()

    def test_direct_shard_refuses_incomplete_metadata_before_starting_tests(self):
        stage = {"kind": "go", "packages": ["./..."], "exclude": "", "options": [], "flags": []}
        for records in ["", '{"ImportPath":"module/a","DepsErrors":[{}]}']:
            with patch.dict(execution.os.environ, {"PW_GO_SHARD": "1/2"}), \
                    patch.object(execution.subprocess, "run", return_value=type("Result", (), {"stdout": records})()), \
                    self.assertRaises(ValueError):
                execution.direct_packages(stage)
        with patch.dict(execution.os.environ, {"PW_GO_SHARD": ""}):
            self.assertEqual(execution.direct_packages(stage), ["./..."])

    def test_lane_uses_task_admission_and_preserves_the_verdict(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for code, status in [(0, "passed"), (1, "failed"), (2, "unverified"), (-15, "unverified")]:
                with patch.object(ci, "artifact_root", return_value=root), \
                        patch.object(ci.subprocess, "call", return_value=code) as run:
                    self.assertEqual(ci.run_lane(DEN), code)
                    run.assert_called_once()
                    self.assertEqual(run.call_args.args[0], ["./task", "den:typecheck", "den:lint", "den:knip:changes", "den:coverage:changes"])
                    self.assertEqual(run.call_args.kwargs["cwd"], ci.ROOT)
                record = json.loads((root / "ci/run.json").read_text())
                self.assertEqual(record["status"], status)
                self.assertGreaterEqual(record["finished_at"], record["started_at"])

    def test_lane_arguments_reach_its_single_target(self):
        data = copy.deepcopy(hosted_plan.catalog())
        data["ci"][CONFINE]["arguments"] = ["nix"]
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(hosted_plan, "catalog", return_value=data), \
                patch.object(ci, "artifact_root", return_value=Path(directory)), \
                patch.object(ci.subprocess, "call", return_value=0) as run:
            ci.run_lane(CONFINE)
            self.assertEqual(run.call_args.args[0], ["./task", "test:confine:linux", "--", "nix"])
            ci.run_lane(NIX)
            self.assertEqual(run.call_args.args[0], ["./task", "nix:check"])

    def test_a_lane_with_arguments_runs_each_target_as_its_own_valid_selection(self):
        lane = next(name for name, spec in hosted_plan.lanes().items()
                    if "arguments" in spec and len(hosted_plan.invocation(spec["targets"])) > 1)
        spec = hosted_plan.lanes()[lane]
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(ci, "artifact_root", return_value=Path(directory)), \
                patch.object(ci.subprocess, "call", return_value=0) as run:
            self.assertEqual(ci.run_lane(lane), 0)
        commands = [call.args[0] for call in run.call_args_list]
        self.assertEqual(commands, [["./task", target, "--", *spec["arguments"]]
                                    for target in hosted_plan.invocation(spec["targets"])])
        for command in commands:
            split = command.index("--")
            verification_plan.validate_selection(command[1:split], command[split + 1:])

    def test_the_changed_package_lane_runs_only_the_edited_packages(self):
        lane = live(phase="changed-packages")
        scope = {"base": "b" * 40, "head": "h" * 40, "full": False, "reasons": [],
                 "paths": ["lycaon/internal/confine/seatbelt_darwin.go", "lycaon/internal/db/store.go"]}
        target = hosted_plan.lanes()[lane]["targets"]
        with tempfile.TemporaryDirectory() as directory, \
                patch.dict(ci.os.environ, {"PW_CI_AFFECTED": "1"}), \
                patch("ci_policy.impact.change", return_value=scope), \
                patch.object(ci, "artifact_root", return_value=Path(directory)), \
                patch.object(ci.subprocess, "call", return_value=0) as run:
            self.assertEqual(ci.run_lane(lane), 0)
            self.assertEqual([call.args[0] for call in run.call_args_list],
                             [["./task", *target, "--", "./internal/confine"]])
        # A change with no Go source left to run leaves the lane unverified, never an empty pass.
        with tempfile.TemporaryDirectory() as directory, \
                patch.dict(ci.os.environ, {"PW_CI_AFFECTED": "1"}), \
                patch("ci_policy.impact.change", return_value={**scope, "paths": ["lycaon/internal/db/store.go"]}), \
                patch.object(ci, "artifact_root", return_value=Path(directory)), \
                patch.object(ci.subprocess, "call", return_value=0) as run:
            self.assertEqual(ci.run_lane(lane), 2)
            run.assert_not_called()

    def test_lane_worker_cap_reaches_admission(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(ci, "artifact_root", return_value=Path(directory)), \
                patch.object(ci.subprocess, "call", return_value=0) as run:
            ci.run_lane(BEHAVIOR, "1/3")
            self.assertEqual(run.call_args.kwargs["env"]["PW_TEST_WORKERS"], str(hosted_plan.lanes()[BEHAVIOR]["workers"]))
            ci.run_lane(DEN)
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
        count = hosted_plan.lanes()[RACE]["shards"]
        rows = [row for row in hosted_plan.matrix("nightly", "race")["include"] if row["lane"] == RACE]
        self.assertEqual([row["shard"] for row in rows], [f"{k}/{count}" for k in range(1, count + 1)])
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(ci, "artifact_root", return_value=Path(directory)), \
                patch.object(ci.subprocess, "call", return_value=0) as run:
            ci.run_lane(RACE, f"2/{count}")
            self.assertEqual(run.call_args.kwargs["env"]["PW_GO_SHARD"], f"2/{count}")
            for lane, shard in [(RACE, ""), (RACE, f"{count + 1}/{count}"), (DEN, "1/2")]:
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
        # Sparse selections retain disjoint shares; a validated empty share starts no digest.
        self.assertEqual([shard_packages(["a", "b"], f"{k}/3") for k in range(1, 4)], [["a"], ["b"], []])

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
            ci.run_lane(BEHAVIOR, "2/3")
            environment = run.call_args.kwargs["env"]
            self.assertNotIn("PW_CI_PACKAGE_TIMINGS", environment)
            self.assertEqual(Path(environment["PW_GO_TIMINGS"]).read_text(), measured)
            ci.run_lane(DEN)
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
            ci.run_lane(BEHAVIOR, "1/3")
        self.assertEqual(run.call_args.args[0], ["./task", "test:full", "--", "./internal/a", "./test/contract"])
        self.assertEqual(run.call_args.kwargs["env"]["PW_GO_SHARD"], "1/3")

    def test_lane_budget_bounds_the_go_watchdog_unless_the_caller_sets_one(self):
        minutes = hosted_plan.lanes()[BEHAVIOR]["minutes"]
        with tempfile.TemporaryDirectory() as directory:
            for inherited, expected in [({}, str(minutes * 60)), ({"PW_GO_TEST_TIMEOUT_SECONDS": "60"}, "60")]:
                with patch.object(ci, "artifact_root", return_value=Path(directory)), \
                        patch.dict(ci.os.environ, inherited), \
                        patch.object(ci.subprocess, "call", return_value=0) as run:
                    if not inherited:
                        ci.os.environ.pop("PW_GO_TEST_TIMEOUT_SECONDS", None)
                    ci.run_lane(BEHAVIOR, "1/3", clock=lambda: 0)
                    self.assertEqual(run.call_args.kwargs["env"]["PW_GO_TEST_TIMEOUT_SECONDS"], expected)

    def test_each_phase_watchdog_gets_what_is_left_of_the_lane_budget(self):
        seconds = hosted_plan.lanes()[ADMISSION_TESTS]["minutes"] * 60
        # The lane starts at 0; its phases start 100 seconds in and 40 seconds past the budget.
        readings = iter([0, 100, seconds + 40])
        phases = [["./task", "test:contract"], ["./task", "test:short"]]
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(ci, "artifact_root", return_value=Path(directory)), \
                patch.object(ci.ready_scope, "test_phases", return_value=(phases, [])), \
                patch.object(ci.subprocess, "call", return_value=0) as run:
            ci.os.environ.pop("PW_GO_TEST_TIMEOUT_SECONDS", None)
            ci.run_lane(ADMISSION_TESTS, "1/4", clock=lambda: next(readings))
        budgets = [call.kwargs["env"]["PW_GO_TEST_TIMEOUT_SECONDS"] for call in run.call_args_list]
        self.assertEqual(budgets, [str(seconds - 100), "1"])
        self.assertEqual([call.kwargs["env"]["PW_TEST_WATCHDOG_DEADLINE"] for call in run.call_args_list],
                         [str(seconds), str(seconds)])

    def test_interrupted_invocation_leaves_an_unfinished_record(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with patch.object(ci, "artifact_root", return_value=root), \
                    patch.object(ci.subprocess, "call", side_effect=KeyboardInterrupt), \
                    self.assertRaises(KeyboardInterrupt):
                ci.run_lane(STRESS)
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

    def test_full_build_lane_receives_structured_change_base(self):
        scope = dict(base='b' * 40, head='a' * 40, paths=['lycaon/internal/a/a.go'], full=True, reasons=[])
        with tempfile.TemporaryDirectory() as directory, \
                patch.dict('os.environ', PW_CI_AFFECTED='0'), \
                patch('ci_policy.impact.change', return_value=scope), \
                patch.object(ci, 'artifact_root', return_value=Path(directory)), \
                patch.object(ci.subprocess, 'call', return_value=0) as command:
            ci.run_lane(BUILD)
        self.assertEqual(command.call_args.kwargs['env']['PW_CHANGE_BASE'], 'b' * 40)

    def test_fast_lanes_run_every_phase_and_keep_the_worst_verdict(self):
        scope = dict(base='b' * 40, head='a' * 40, paths=[], full=False)
        phases = [['./task', 'one'], ['./task', 'two'], ['./task', 'three']]
        for name, target in ((ADMISSION, 'check_phases'), (ADMISSION_TESTS, 'test_phases')):
            value = phases if target == 'check_phases' else (phases, None)
            for verdicts, expected in (([1, 2, 0], 1), ([2, 1, 0], 1), ([0, 2, 0], 2), ([0, 0, 0], 0)):
                with self.subTest(name=name, verdicts=verdicts), tempfile.TemporaryDirectory() as directory, \
                        patch('ci_policy.impact.change', return_value=scope), \
                        patch(f'ci_policy.ready_scope.{target}', return_value=value), \
                        patch.object(ci, 'artifact_root', return_value=Path(directory)), \
                        patch.object(ci.subprocess, 'call', side_effect=verdicts) as run:
                    self.assertEqual(ci.run_lane(name, '1/4' if name == ADMISSION_TESTS else ''), expected)
                    self.assertEqual(run.call_count, 3)
                    self.assertEqual(run.call_args.kwargs['env']['PW_TEST_KEEP_GOING'], '1')
                    record = json.loads((Path(directory) / 'ci/run.json').read_text())
                    self.assertEqual([phase['exit_code'] for phase in record['phases']], verdicts)
                    self.assertEqual(record['status'], {0: 'passed', 1: 'failed', 2: 'unverified'}[expected])

    def test_the_den_lane_reports_every_failing_stage(self):
        scope = dict(base='b' * 40, head='a' * 40, paths=['lycaon-den/src/app.ts'], full=False)
        with tempfile.TemporaryDirectory() as directory, \
                patch.dict('os.environ', PW_CI_AFFECTED='1'), \
                patch('ci_policy.impact.change', return_value=scope), \
                patch.object(ci, 'artifact_root', return_value=Path(directory)), \
                patch.object(ci.subprocess, 'call', return_value=1) as run:
            self.assertEqual(ci.run_lane(DEN), 1)
        self.assertEqual(run.call_args.args[0][1:], ['den:typecheck', 'den:lint', 'den:knip:changes', 'den:coverage:changes'])
        self.assertEqual(run.call_args.kwargs['env']['PW_TEST_KEEP_GOING'], '1')

    def test_outside_a_change_base_lanes_run_the_suites_their_gates_measure(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(ci, 'artifact_root', return_value=Path(directory)), \
                patch.object(ci.subprocess, 'call', return_value=0) as run:
            ci.run_lane(DEN, profile='integration')
            self.assertEqual(run.call_args.args[0], ['./task', 'den:typecheck', 'den:lint', 'den:test'])
            ci.run_lane(SCRIPTS, profile='nightly')
            self.assertEqual(run.call_args.args[0], ['./task', 'test:runner', '--', '--every-suite'])
            ci.run_lane(SCRIPTS)
            self.assertEqual(run.call_args.args[0], ['./task', 'scripts:coverage:changes'])

    def test_fast_lane_stops_when_a_phase_is_interrupted(self):
        scope = dict(base='b' * 40, head='a' * 40, paths=[], full=False)
        with tempfile.TemporaryDirectory() as directory, \
                patch('ci_policy.impact.change', return_value=scope), \
                patch('ci_policy.ready_scope.check_phases', return_value=[['./task', 'one'], ['./task', 'two'], ['./task', 'three']]), \
                patch.object(ci, 'artifact_root', return_value=Path(directory)), \
                patch.object(ci.subprocess, 'call', side_effect=[0, -15, 0]) as run:
            self.assertEqual(ci.run_lane(ADMISSION), -15)
        self.assertEqual(run.call_count, 2)

    def test_interrupted_lane_keeps_an_unfinished_record(self):
        scope = dict(base="b" * 40, head="a" * 40, paths=[], full=False)
        with tempfile.TemporaryDirectory() as directory, \
                patch("ci_policy.impact.change", return_value=scope), \
                patch.object(ci, "artifact_root", return_value=Path(directory)), \
                patch.object(ci.subprocess, "call", side_effect=KeyboardInterrupt) as run:
            with self.assertRaises(KeyboardInterrupt):
                ci.run_lane(ADMISSION)
            self.assertEqual(run.call_count, 1)
            record = json.loads((Path(directory) / "ci/run.json").read_text())
            self.assertEqual(record["status"], "running")
            self.assertNotIn("exit_code", record)

    def test_every_lane_reports_every_failure(self):
        scope = dict(base='b' * 40, head='a' * 40, paths=[], full=False)
        with tempfile.TemporaryDirectory() as directory, \
                patch('ci_policy.impact.change', return_value=scope), \
                patch.object(ci, 'artifact_root', return_value=Path(directory)), \
                patch.object(ci.subprocess, 'call', return_value=1) as run:
            self.assertEqual(ci.run_lane(BUILD), 1)
        self.assertEqual(run.call_count, 1)
        self.assertEqual(run.call_args.kwargs['env']['PW_TEST_KEEP_GOING'], '1')

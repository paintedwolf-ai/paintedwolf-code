"""Merge phases preserve the full recipe and stop before expensive work on failure."""
import json
import subprocess
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import merge_preflight as preflight
from ci_policy import hosted_plan


class MergePreflightTests(unittest.TestCase):
    def test_fixed_platform_arguments_survive_merge_preflight(self):
        for arguments in (["launch"], ["nix"]):
            lane = {"targets": ["test:confine:linux"], "arguments": arguments}
            self.assertEqual(preflight.commands(lane, lane["targets"]),
                             [["./task", *lane["targets"], "--", *lane["arguments"]]])

    def test_packages_are_partitioned_once_with_contracts_first(self):
        packages = ["module/internal/a", "module/test/contract/hosted", "module/test/contract", "module/test/smoke"]
        with patch.object(preflight.subprocess, "run") as run:
            run.return_value.stdout = "\n".join(json.dumps({"ImportPath": name}) for name in packages)
            phases = preflight.package_phases(None)
            self.assertEqual(phases[0], ["module/test/contract", "module/test/contract/hosted"])
            self.assertEqual(sorted(sum(phases, [])), sorted(packages))
            self.assertEqual(len(sum(phases, [])), len(packages))
            self.assertEqual(run.call_args.args[0][-1], "./...")
            self.assertEqual(run.call_args.kwargs["env"]["GOPROXY"], "off")
            self.assertEqual(run.call_args.kwargs["env"]["GOTOOLCHAIN"], "local")
            preflight.package_phases(["./internal/a", "./test/contract/..."])
            self.assertEqual(run.call_args.args[0][-2:], ["./internal/a", "./test/contract/..."])

    def test_uncertain_or_incomplete_selection_cannot_skip_contracts(self):
        for payload in ("", '{"ImportPath":"module/internal/a"}',
                        '{"ImportPath":"module/test/contract/a","DepsErrors":[{}]}'):
            with self.subTest(payload=payload), patch.object(preflight.subprocess, "run") as run:
                run.return_value.stdout = payload
                with self.assertRaises(ValueError):
                    preflight.package_phases(None)

    def test_catalog_orders_diagnostics_without_repeating_targets(self):
        lane = hosted_plan.lanes()["lint"]
        commands = preflight.commands(lane, lane["targets"])
        self.assertEqual(commands[0], ["./task", "comments:check", "test:fail-messages:check"])
        self.assertEqual(commands[1], ["./task", "lint:full"])
        self.assertEqual(sorted([target for command in commands for target in command[1:]]), sorted(lane["targets"]))
        with patch.object(preflight, "package_phases", return_value=[["contract"], ["behavior"]]):
            self.assertEqual(preflight.commands(hosted_plan.lanes()["behavior"], ["test:full"]),
                             [["./task", "test:full", "--", "contract"], ["./task", "test:full", "--", "behavior"]])

    def test_failed_contract_phase_prevents_behavior_and_preserves_failure_evidence(self):
        from ci_policy import hosted_execution
        for code in (1, 2, -15):
            with self.subTest(code=code), tempfile.TemporaryDirectory() as directory, \
                    patch.object(hosted_execution, "artifact_root", return_value=Path(directory)), \
                    patch.object(preflight, "package_phases", return_value=[["contract"], ["behavior"]]), \
                    patch.object(hosted_execution.subprocess, "call", return_value=code) as run:
                self.assertEqual(hosted_execution.run_lane("behavior", "1/2", merge_group=True), code)
                run.assert_called_once()
                self.assertEqual(run.call_args.args[0], ["./task", "test:full", "--", "contract"])
                self.assertEqual(run.call_args.kwargs["env"]["PW_GO_SHARD"], "1/2")
                record = json.loads((Path(directory) / "ci/run.json").read_text())
                self.assertEqual(record["phases"][0]["exit_code"], code)
                self.assertEqual(len(record["phases"]), 1)

    def test_failed_package_planning_finalizes_unverified_lane_evidence(self):
        from ci_policy import hosted_execution
        errors = (subprocess.CalledProcessError(1, ["go", "list"], stderr="dependency unavailable"),
                  ValueError("incomplete metadata"), FileNotFoundError("go unavailable"))
        for error in errors:
            with self.subTest(error=type(error).__name__), tempfile.TemporaryDirectory() as directory, \
                    patch.object(hosted_execution, "artifact_root", return_value=Path(directory)), \
                    patch.object(preflight, "package_phases", side_effect=error), \
                    patch.object(hosted_execution.subprocess, "call") as run:
                self.assertEqual(hosted_execution.run_lane("behavior", "1/2", merge_group=True), 2)
                run.assert_not_called()
                record = json.loads((Path(directory) / "ci/run.json").read_text())
                self.assertEqual(record["status"], "unverified")
                self.assertEqual(record["phases"], [])
                self.assertEqual(record["execution_error"]["type"], type(error).__name__)
                self.assertGreaterEqual(record["finished_at"], record["started_at"])
                if isinstance(error, subprocess.CalledProcessError):
                    self.assertEqual(record["execution_error"]["stderr"], error.stderr)

    def test_throughput_counts_actual_merges_and_separates_cancellation(self):
        from ci_policy.health import throughput
        runs = [dict(id=n, conclusion=conclusion, created_at="2026-10-22T10:00:00Z", updated_at="2026-10-22T10:10:00Z")
                for n, conclusion in enumerate(("success", "success", "failure", "cancelled"))]
        jobs = lambda run: [dict(id=run["id"], started_at="2026-10-22T10:01:00Z", completed_at="2026-10-22T10:10:00Z")]
        report = throughput(runs, [{"number": 1}, {"number": 1}], "2026-10-22T10:00:00Z", "2026-10-22T12:00:00Z", jobs)
        self.assertEqual(report["main_merges_per_hour"], .5)
        self.assertEqual(report["decided_groups"], 3)
        self.assertEqual(report["failure_fraction_of_decided"], 1 / 3)
        self.assertEqual(report["merge_group_outcomes"]["cancelled"], 1)
        self.assertEqual(report["created_to_failure_p95_seconds"], 600)
        self.assertEqual(report["observed_runner_minutes"], 36)

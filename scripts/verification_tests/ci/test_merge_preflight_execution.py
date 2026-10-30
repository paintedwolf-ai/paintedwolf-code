"""Merge phases preserve the full recipe and stop before expensive work on failure."""
import io
import json
import zipfile
from datetime import datetime, timedelta, timezone
import os
import subprocess
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from ci_policy import merge_preflight as preflight
from ci_policy import hosted_plan


class MergePreflightTests(unittest.TestCase):
    def test_fast_coverage_measurements_ignore_unrelated_paths(self):
        import coverage_changes as coverage
        for paths in (['docs/README.md'], ['lycaon/internal/example.go'], ['scripts/example.sh'],
                      ['lycaon-den/src/model.test.ts', 'lycaon-den/src/style.css']):
            with self.subTest(paths=paths), patch.object(coverage.subprocess, 'call') as call, \
                    patch.object(coverage.python_coverage, 'measure') as measure:
                scope = SimpleNamespace(paths=paths)
                self.assertEqual(coverage.run_den(scope), ([], False, 'no changed frontend source'))
                self.assertEqual(coverage.run_python(scope), ([], False, 'no changed scripts Python'))
                call.assert_not_called()
                measure.assert_not_called()

    def history_archive(self, record=None, failures=None):
        record = record if record is not None else {
            'lane': 'ready', 'shard': '', 'status': 'failed',
            'identity': {'run': '20', 'attempt': '2', 'sha': 'synthetic-merge'}}
        failures = failures if failures is not None else [{'stage': 'budgets', 'status': 'failed'}]
        output = io.BytesIO()
        with zipfile.ZipFile(output, 'w') as archive:
            archive.writestr('ci/run.json', json.dumps(record))
            archive.writestr('ci/failures.json', json.dumps(failures))
        return output.getvalue()

    def test_previous_failure_is_bound_to_workflow_pull_and_attempt(self):
        event = {'pull_request': {'number': 7, 'head': {'ref': 'fix/a&b'}}}
        previous = dict(id=20, workflow_id=10, event='pull_request', pull_requests=[{'number': 7}],
                        run_attempt=2, status='completed', conclusion='failure', head_sha='pr-head')
        artifact = dict(id=40, name='receipt-verification-fast-ready-2', expired=False, size_in_bytes=200)
        unrelated = [{**previous, 'id': 25, 'pull_requests': [{'number': 8}]},
                     {**previous, 'id': 26, 'workflow_id': 11}, {**previous, 'id': 30}]
        cases = [(previous, 'budgets'), ({**previous, 'conclusion': 'success'}, ''),
                 ({**previous, 'conclusion': 'cancelled'}, ''), ({**previous, 'status': 'in_progress'}, '')]
        for run, expected in cases:
            with self.subTest(run=run), patch.object(preflight, 'repository', return_value='repos/o/r'), \
                    patch.object(preflight, 'api', side_effect=[{'workflow_id': 10},
                        {'workflow_runs': [run, {**previous, 'id': 19}, *unrelated]}, {'artifacts': [artifact]}]) as api, \
                    patch.object(preflight.subprocess, 'check_output', return_value=self.history_archive()) as fetch:
                self.assertEqual(preflight.previous_target(event, '30', ['budgets']), expected)
                self.assertEqual(fetch.call_count, int(bool(expected)))
                self.assertIn('/workflows/10/runs?event=pull_request', api.call_args_list[1].args[0])
        for event in ({}, {'pull_request': {}}):
            with patch.object(preflight, 'api') as api:
                self.assertEqual(preflight.previous_target(event, '30', ['budgets']), '')
                api.assert_not_called()

    def test_uncertain_history_never_reorders(self):
        event = {'pull_request': {'number': 7, 'head': {'ref': 'fix/a&b'}}}
        run = dict(id=20, run_attempt=2, workflow_id=10, event='pull_request',
                   pull_requests=[{'number': 7}], status='completed', conclusion='failure')
        artifact = dict(id=40, name='receipt-verification-fast-ready-2', expired=False, size_in_bytes=200)
        for artifacts, raw in [([], b''), ([artifact, artifact], b''),
                               ([{**artifact, 'expired': True}], b''),
                               ([{**artifact, 'size_in_bytes': preflight.HISTORY_LIMIT + 1}], b''),
                               ([artifact], b'not a zip')]:
            with patch.object(preflight, 'repository', return_value='repos/o/r'), \
                    patch.object(preflight, 'api', side_effect=[{'workflow_id': 10},
                        {'workflow_runs': [run]}, {'artifacts': artifacts}]), \
                    patch.object(preflight.subprocess, 'check_output', return_value=raw):
                self.assertEqual(preflight.previous_target(event, '30', ['budgets']), '')
        with patch.object(preflight, 'repository', return_value='repos/o/r'), \
                patch.object(preflight, 'api', side_effect=OSError):
            self.assertEqual(preflight.previous_target(event, '30', ['budgets']), '')

    def test_branch_query_finds_history_behind_unrelated_workflow_traffic(self):
        run = dict(id=20, workflow_id=10, event='pull_request', pull_requests=[{'number': 7}],
                   run_attempt=2, status='completed', conclusion='failure')
        artifact = dict(id=40, name='receipt-verification-fast-ready-2', expired=False, size_in_bytes=200)
        def api(path):
            if '/workflows/' in path:
                branch_scoped = 'branch=fix%2Fa%26b&per_page=100' in path
                return {'workflow_runs': [run] if branch_scoped else [{**run, 'pull_requests': [{'number': 8}]}] * 100}
            return {'artifacts': [artifact]} if '/artifacts?' in path else {'workflow_id': 10}
        with patch.object(preflight, 'repository', return_value='repos/o/r'), \
                patch.object(preflight, 'api', side_effect=api), \
                patch.object(preflight.subprocess, 'check_output', return_value=self.history_archive()):
            event = {'pull_request': {'number': 7, 'head': {'ref': 'fix/a&b'}}}
            self.assertEqual(preflight.previous_target(event, '30', ['budgets']), 'budgets')

    def test_receipt_requires_matching_identity_and_one_declared_failed_stage(self):
        run = {'id': 20, 'run_attempt': 2}
        record = dict(lane='ready', shard='', status='failed', identity={'run': '20', 'attempt': '2'})
        for change in ({'lane': 'limits'}, {'status': 'passed'}, {'shard': '1/2'},
                       {'identity': {'run': '21', 'attempt': '2'}}, {'identity': {'run': '20', 'attempt': '1'}}):
            self.assertEqual(preflight.failed_target(self.history_archive({**record, **change}), run, ['budgets']), '')
        for failures in ([], [None], [{'stage': 'test:contract', 'status': 'failed'}],
                         [{'stage': 'budgets', 'status': 'unverified'}],
                         [{'stage': stage, 'status': 'failed'} for stage in ('budgets', 'build')]):
            self.assertEqual(preflight.failed_target(self.history_archive(failures=failures), run, ['budgets', 'build']), '')
        repeated = [{'stage': 'budgets', 'status': 'failed'}] * 2
        self.assertEqual(preflight.failed_target(self.history_archive(failures=repeated), run, ['budgets']), 'budgets')

    def test_ready_history_checks_repeat_failure_before_native_compilation(self):
        from ci_policy import hosted_execution as execution
        scope = dict(base='b' * 40, head='a' * 40, paths=[], full=False)
        targets = hosted_plan.lanes()['ready']['targets']
        for early, hosted, verdict in [('budgets', 'true', 1), ('budgets', 'true', 0),
                                      ('comments:check', 'true', 1), ('comments:check', 'true', 0),
                                      ('budgets', 'true', 2), ('budgets', 'true', -15), ('', 'true', 0), ('unknown', 'true', 0), ('budgets', 'false', 0)]:
            active = early in targets and hosted == 'true'
            with self.subTest(early=early, hosted=hosted, verdict=verdict), tempfile.TemporaryDirectory() as directory, \
                    patch.object(execution, 'artifact_root', return_value=Path(directory)), \
                    patch('ci_policy.impact.change', return_value=scope), \
                    patch('ci_policy.native_admission.commands', return_value=[['./task', 'decide:test', '--', '--compile-only']]), \
                    patch.dict(os.environ, GITHUB_ACTIONS=hosted, PW_CI_EARLY_TARGET=early), \
                    patch.object(execution.subprocess, 'call', side_effect=lambda cmd, **_: verdict if cmd == ['./task', early] else 0) as call:
                self.assertEqual(execution.run_lane('ready'), verdict)
                commands = [entry.args[0] for entry in call.call_args_list]
                expected = [['./task', *hosted_plan.lanes()['ready']['preflight']]]
                compile_native = ['./task', 'decide:test', '--', '--compile-only']
                if active:
                    expected += [['./task', early]]
                    if not verdict:
                        expected += [compile_native, ['./task', *[target for target in targets if target != early]]]
                else:
                    expected += [compile_native, ['./task', *targets]]
                self.assertEqual(commands, expected)
                record = json.loads((Path(directory) / 'ci/run.json').read_text())
                self.assertEqual([phase['command'] for phase in record['phases']], expected[1:])

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
        jobs = lambda run: [dict(id=run["id"], runner_id=1, started_at="2026-10-22T10:01:00Z", completed_at="2026-10-22T10:10:00Z"),
                            dict(id=100 + run["id"], runner_id=1, conclusion="skipped", started_at="2026-10-22T10:01:00Z", completed_at="2026-10-22T10:10:00Z")]
        report = throughput(runs, [{"number": 1}, {"number": 1}], "2026-10-22T10:00:00Z", "2026-10-22T12:00:00Z", jobs)
        self.assertEqual(report["main_merges_per_hour"], .5)
        self.assertEqual(report["decided_groups"], 3)
        self.assertEqual(report["failure_fraction_of_decided"], 1 / 3)
        self.assertEqual(report["merge_group_outcomes"]["cancelled"], 1)
        self.assertEqual(report["created_to_failure_p95_seconds"], 600)
        self.assertEqual(report["observed_runner_minutes"], 36)

    def test_queue_health_queries_batch_identity_for_original_delivery(self):
        from ci_policy import health
        pull = {"number": 12, "baseRefName": "main", "mergedAt": "2026-10-22T12:00:00Z",
                "updatedAt": "2026-10-22T12:00:00Z"}
        response = {"data": {"repository": {"pullRequests": {
            "pageInfo": {"hasNextPage": False, "endCursor": None}, "nodes": [pull]}}}}
        with patch.dict(os.environ, {"GITHUB_REPOSITORY": "o/r"}), patch.object(health, "api", return_value=response) as api:
            self.assertEqual(health.recent_merges("2026-10-22T10:00:00Z"), [pull])
        query = api.call_args.args[2]["query"]
        for field in ("headRefName", "body", "author {login}"):
            self.assertIn(field, query)

    def test_queue_health_report_counts_queue_dispatches_and_reuses_job_inventory(self):
        from ci_policy import health
        now = datetime.now(timezone.utc)
        stamp = lambda minutes: (now - timedelta(minutes=minutes)).strftime("%Y-%m-%dT%H:%M:%SZ")
        def run(number, event, branch):
            return {"id": number, "event": event, "head_branch": branch, "status": "completed",
                    "conclusion": "success", "created_at": stamp(8), "updated_at": stamp(2),
                    "html_url": f"https://example.test/runs/{number}"}
        runs = [run(1, "merge_group", "gh-readonly-queue/main/pr-1"),
                run(2, "workflow_dispatch", "gh-readonly-queue/main/pr-2"),
                run(3, "workflow_dispatch", "main")]
        calls = []
        def pages(path, *_):
            calls.append(path)
            if "/workflows/ci.yml/runs?" in path:
                return iter(runs)
            return iter([{"id": int(path.split("/runs/")[1].split("/")[0]), "runner_id": 1,
                          "name": "integration / limits", "conclusion": "success", "steps": [],
                          "created_at": stamp(8), "started_at": stamp(7), "completed_at": stamp(2)}])
        with tempfile.TemporaryDirectory() as directory, patch.object(health, "pages", side_effect=pages), \
                patch.object(health, "repository", return_value="repos/o/r"), \
                patch.object(health, "recent_merges", return_value=[]), \
                patch.object(health, "evidence", return_value=([], [])), \
                patch.dict(os.environ, {"GITHUB_STEP_SUMMARY": str(Path(directory) / "summary.md")}):
            before = Path.cwd()
            try:
                os.chdir(directory)
                health.main()
                report = json.loads(Path("queue-health.json").read_text())
            finally:
                os.chdir(before)
        self.assertEqual(report["runs_observed"], 2)
        self.assertEqual(report["throughput"]["decided_groups"], 2)
        self.assertEqual(report["job_costs"]["totals"]["runner_seconds"], 600)
        self.assertEqual(sum("/jobs?filter=all" in path for path in calls), 2)

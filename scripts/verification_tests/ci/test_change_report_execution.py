"""Change-scoped reports: the change base, budget findings, and changed-statement coverage."""

import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from contextlib import redirect_stdout
from unittest.mock import patch

import budgets
import change_report
import coverage_changes
import coverage_policy


def git(root, *args):
    subprocess.run(["git", "-C", str(root), *args], check=True, capture_output=True, text=True)


class ChangeScopeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        git(self.root, "init", "-q", "-b", "main")
        git(self.root, "config", "user.email", "test@example.com")
        git(self.root, "config", "user.name", "Test")
        (self.root / "kept.go").write_text("a\nb\nc\nd\n")
        (self.root / "dir").mkdir()
        (self.root / "dir" / "old.ts").write_text("x\n")
        git(self.root, "add", ".")
        git(self.root, "commit", "-q", "-m", "base")
        git(self.root, "checkout", "-q", "-b", "feature")

    def test_scope_measures_the_branch_from_its_merge_base_including_untracked_work(self):
        (self.root / "kept.go").write_text("a\nB\nc\nd\ne\n")
        git(self.root, "commit", "-qam", "edit")
        (self.root / "dir" / "new.ts").write_text("1\n2\n")
        with patch.dict(os.environ, {}, clear=False):
            os.environ.pop(change_report.BASE_ENV, None)
            scope = change_report.resolve(self.root)
        self.assertEqual(scope.label, "merge base with main")
        self.assertEqual(scope.paths, ["dir/new.ts", "kept.go"])
        self.assertTrue(scope.touches(["dir"]))
        self.assertTrue(scope.touches(["kept.go"]))
        self.assertFalse(scope.touches(["dir/old.ts", "di"]))
        lines = change_report.changed_lines(self.root, scope.base, scope.paths)
        self.assertEqual(lines, {"kept.go": {2, 5}, "dir/new.ts": {1, 2}})
        git(self.root, "rm", "-q", "dir/old.ts")
        self.assertEqual(change_report.added_and_removed(self.root, scope.base), (["dir/new.ts"], ["dir/old.ts"]))

    def test_explicit_base_must_be_a_commit(self):
        with patch.dict(os.environ, {change_report.BASE_ENV: "no-such-ref"}):
            with self.assertRaisesRegex(RuntimeError, "is not a commit"):
                change_report.resolve(self.root)

    def test_no_main_branch_means_no_scope(self):
        git(self.root, "branch", "-q", "-m", "main", "trunk")
        with patch.dict(os.environ, {}, clear=False):
            os.environ.pop(change_report.BASE_ENV, None)
            self.assertIsNone(change_report.resolve(self.root))

    def test_ranges_collapse_consecutive_lines(self):
        self.assertEqual(change_report.ranges({7, 1, 2, 3, 5}), [(1, 3), (5, 5), (7, 7)])

    def test_github_annotations_and_summary(self):
        summary = self.root / "summary.md"
        (self.root / "f.go").write_text("x\n")
        findings = [change_report.Finding("error", "go", "uncovered", "f.go", 3, 9),
                    change_report.Finding("info", "go", "context")]
        out = io.StringIO()
        with patch.dict(os.environ, {"GITHUB_ACTIONS": "true", "GITHUB_STEP_SUMMARY": str(summary)}), redirect_stdout(out):
            change_report.emit(self.root, "Coverage", "header", findings, True)
        self.assertIn("::error file=f.go,line=3,endLine=9,title=Coverage (go)::uncovered", out.getvalue())
        self.assertNotIn("::info", out.getvalue())
        self.assertIn("### Coverage: failed", summary.read_text())
        self.assertIn("- go: context", summary.read_text())


def report(findings, untouched=None, notes=(), warnings=()):
    return {"policy": "policy.yaml", "categories": {"files": {"unit": "lines", "warn": 10, "limit": 20}},
            "findings": findings, "untouched": untouched or {}, "notes": list(notes), "warnings": list(warnings)}


def finding(kind, artifact, measured, bound, **extra):
    return {"category": "files", "id": artifact, "kind": kind, "measured": measured, "bound": bound,
            "sources": [artifact], **extra}


class BudgetReportTests(unittest.TestCase):
    def test_touched_artifacts_past_a_line_fail_and_name_their_source(self):
        findings = [finding("over_limit", "big.go", 25, 20),
                    finding("over_cap", "special.go", 51, 50, reason="one table per dialect")]
        lines, failed = budgets.suite_findings("maintainability", report(findings))
        self.assertTrue(failed)
        self.assertEqual([(f.level, f.path) for f in lines], [("error", "big.go"), ("error", "special.go")])
        self.assertIn("past its limit of 20", lines[0].text)
        self.assertIn("revisit the reason: one table per dialect", lines[1].text)

    def test_signals_before_the_limit_do_not_fail(self):
        findings = [finding("over_warn", "warm.go", 15, 10),
                    finding("excepted", "shell.tsx", 40, 50, reason="composition root")]
        lines, failed = budgets.suite_findings("maintainability", report(findings, untouched={"files": 3}))
        self.assertFalse(failed)
        self.assertEqual([f.level for f in lines], ["warning", "notice", "info"])
        self.assertIn("past the warning line of 10", lines[0].text)
        self.assertIn("admitted up to 50 because: composition root", lines[1].text)
        self.assertIn("3 artifact(s) past their limit were not touched", lines[2].text)

    def test_exception_changes_and_headroom_are_reported(self):
        findings = [finding("exception_added", "big.go", 40, 0, reason="one table per dialect", previous=30),
                    finding("unneeded", "small.go", 12, 20, reason="r")]
        lines, failed = budgets.suite_findings("prompts", report(findings, warnings=["coordinator: 8% headroom"],
                                                                 notes=["worker: 36% headroom"]))
        self.assertFalse(failed)
        self.assertEqual([f.level for f in lines], ["notice", "notice", "warning", "info"])
        self.assertIn("exception raised from 30 to 40; reason: one table per dialect", lines[0].text)
        self.assertIn("remove its exception", lines[1].text)


class ChangedCoverageTests(unittest.TestCase):
    floor = {"percent": coverage_policy.percentage(70), "grace": 2}

    def test_small_gaps_pass_and_large_uncovered_changes_fail(self):
        small = coverage_changes.Unit("pkg/small")
        small.add("small.go", 3, 4, 2, False, {3})
        small.add("small.go", 5, 5, 1, True, {5})
        large = coverage_changes.Unit("pkg/large")
        large.add("large.go", 10, 12, 3, False, {10, 11, 12})
        large.add("large.go", 20, 20, 1, True, {20})
        findings, failed = coverage_changes.judge("go", {"s": small, "l": large}, self.floor)
        self.assertTrue(failed)
        errors = [f for f in findings if f.level == "error"]
        self.assertIn("pkg/large: 1 of 4 changed statements covered (25%)", errors[0].text)
        self.assertEqual((errors[1].path, errors[1].line, errors[1].end_line), ("large.go", 10, 12))
        notices = [f for f in findings if f.level == "notice"]
        self.assertEqual([(f.path, f.line, f.end_line) for f in notices], [("small.go", 3, 3)])

    def test_well_covered_change_fails_nothing(self):
        unit = coverage_changes.Unit("pkg/ok")
        for line in range(1, 11):
            unit.add("ok.go", line, line, 1, line != 1, {line})
        findings, failed = coverage_changes.judge("go", {"ok": unit}, self.floor)
        self.assertFalse(failed)
        self.assertEqual([f.level for f in findings], ["info", "notice"])

    def test_go_profiles_merge_blocks_across_test_binaries(self):
        with tempfile.TemporaryDirectory() as directory:
            profile = Path(directory) / "cover.out"
            profile.write_text("mode: set\n"
                               "example.com/m/internal/a/a.go:3.10,5.2 2 0\n"
                               "example.com/m/internal/a/a.go:3.10,5.2 2 1\n"
                               "example.com/m/internal/a/a.go:7.1,7.9 1 0\n")
            blocks = coverage_changes.read_go_profile(profile, "example.com/m")
        self.assertEqual(sorted(blocks["lycaon/internal/a/a.go"]), [("3.10", "5.2", 2, True), ("7.1", "7.9", 1, False)])

    def test_den_changed_run_asks_the_config_not_the_cli_to_ignore_rerun_triggers(self):
        # Vitest's CLI rejects unknown options, so a rerun-trigger override must reach vitest.config.ts.
        changed = "lycaon-den/src/App.tsx"
        with patch.object(coverage_changes.subprocess, "call", return_value=1) as call:
            coverage_changes.run_den(change_report.Scope("base", "test", [changed]))
        command, env = call.call_args.args[0], call.call_args.kwargs["env"]
        self.assertEqual(env["PW_VITEST_CHANGED_ONLY"], "1")
        self.assertFalse([a for a in command if a.startswith("--forceRerunTriggers")])
        config = (coverage_changes.ROOT / "lycaon-den" / "vitest.config.ts").read_text()
        self.assertIn('process.env.PW_VITEST_CHANGED_ONLY === "1"', config)

    def test_policy_floors_are_read_from_the_policy_file(self):
        floor = coverage_policy.changed("go")
        self.assertGreater(floor["percent"], 0)
        self.assertGreaterEqual(floor["grace"], 0)
        self.assertEqual(coverage_policy.main(["get", "go.total"]), 0)
        self.assertEqual(coverage_policy.main(["get", "go.nothing"]), 2)


if __name__ == "__main__":
    unittest.main()


class BudgetPublicationTests(unittest.TestCase):
    def test_main_publishes_source_bound_inventory_even_when_other_suite_fails(self):
        from ci_policy.budget_snapshot import validate
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'budgets' / 'reports').mkdir(parents=True)
            inventory = {'suite': 'maintainability', 'tracking': {'schema_version': 1, 'complete': True, 'artifacts': []},
                         'findings': [], 'untouched': {}, 'warnings': [], 'notes': []}
            scope = change_report.Scope('c' * 40, 'explicit', [])
            outputs = [subprocess.CompletedProcess([], 0, 'a' * 40), subprocess.CompletedProcess([], 0, 'b' * 40)]
            with patch.object(budgets.change_report, 'resolve', return_value=scope), \
                    patch.object(budgets, 'change_set', return_value={'base': scope.base}), \
                    patch.object(budgets, 'run_suites', return_value=(1, {'maintainability': inventory})), \
                    patch.object(budgets, 'artifact_root', return_value=root), \
                    patch.object(budgets.change_report, 'git', side_effect=outputs), \
                    patch.dict(os.environ, {'GITHUB_SHA': ''}), redirect_stdout(io.StringIO()):
                self.assertEqual(budgets.main(), 1)
            value = json.loads((root / 'budgets' / 'reports' / 'maintainability.json').read_text())
            self.assertEqual(validate(value, 'a' * 40, 'b' * 40)[1], {})

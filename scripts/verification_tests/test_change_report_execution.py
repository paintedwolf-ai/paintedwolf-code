"""Change-scoped reports: the change base, budget findings, and changed-statement coverage."""

import io
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


def report(findings, artifacts, notes=()):
    return {"policy": "policy.yaml", "refresh": "UPDATE=1 ./task budgets",
            "categories": {"files": {"unit": "lines", "warn": 10, "limit": 20}},
            "artifacts": artifacts, "findings": findings, "notes": list(notes)}


class BudgetReportTests(unittest.TestCase):
    def scope(self, *paths):
        return change_report.Scope("a" * 40, "merge base with main", list(paths))

    def test_growth_fails_and_names_its_source(self):
        artifacts = [{"category": "files", "id": "big.go", "measured": 25, "cap": 20, "sources": ["big.go"]}]
        findings = [{"category": "files", "id": "big.go", "kind": "over_limit", "measured": 25, "bound": 20}]
        lines, failed = budgets.suite_findings("maintainability", report(findings, artifacts), self.scope())
        self.assertTrue(failed)
        self.assertEqual([(f.level, f.path) for f in lines], [("error", "big.go")])
        self.assertIn("over its limit of 20 by 5", lines[0].text)

    def test_warnings_and_grandfathered_standing_only_for_touched_artifacts(self):
        artifacts = [
            {"category": "files", "id": "warm.go", "measured": 15, "cap": 20, "sources": ["warm.go"]},
            {"category": "files", "id": "other.go", "measured": 15, "cap": 20, "sources": ["other.go"]},
            {"category": "files", "id": "old.go", "measured": 30, "cap": 30, "entry": "grandfathered", "sources": ["old.go"]},
        ]
        findings = [{"category": "files", "id": i, "kind": "over_warn", "measured": 15, "bound": 10}
                    for i in ("warm.go", "other.go")]
        lines, failed = budgets.suite_findings("maintainability", report(findings, artifacts), self.scope("warm.go", "old.go"))
        self.assertFalse(failed)
        self.assertEqual([(f.level, f.path) for f in lines], [("warning", "warm.go"), ("notice", "old.go")])

    def test_cleanup_is_one_notice_and_notes_pass_through(self):
        findings = [{"category": "files", "id": i, "kind": k, "measured": 0, "bound": 30}
                    for i, k in (("a", "slack"), ("b", "vanished"))]
        lines, failed = budgets.suite_findings("prompts", report(findings, [], ["window headroom"]), None)
        self.assertFalse(failed)
        self.assertEqual([f.level for f in lines], ["notice", "info"])
        self.assertIn("2 entries can be tightened", lines[0].text)

    def test_raised_grandfathered_cap_fails_at_the_policy(self):
        findings = [{"category": "files", "id": "old.go", "kind": "grandfather_raised", "measured": 31, "bound": 30}]
        lines, failed = budgets.suite_findings("maintainability", report(findings, []), None)
        self.assertTrue(failed)
        self.assertEqual(lines[0].path, "policy.yaml")


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
        self.assertEqual(sorted(blocks["lycaon/internal/a/a.go"]), [(3, 5, 2, True), (7, 7, 1, False)])

    def test_policy_floors_are_read_from_the_policy_file(self):
        floor = coverage_policy.changed("go")
        self.assertGreater(floor["percent"], 0)
        self.assertGreaterEqual(floor["grace"], 0)
        self.assertEqual(coverage_policy.main(["get", "go.total"]), 0)
        self.assertEqual(coverage_policy.main(["get", "go.nothing"]), 2)


if __name__ == "__main__":
    unittest.main()

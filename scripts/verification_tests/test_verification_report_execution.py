import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import verification_report as ci
from ci_policy import hosted_plan
import verification_plan as planning
from verification_execute import shard_packages


class VerificationReportTests(unittest.TestCase):
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


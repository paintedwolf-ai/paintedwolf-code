import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import verification_report as ci


class VerificationReportTests(unittest.TestCase):
    def test_every_job_records_the_commit_and_tree_it_checked_out(self):
        with patch.object(ci.subprocess, "check_output", side_effect=["a" * 40 + "\n", "b" * 40 + "\n"]) as git:
            self.assertEqual(ci.source(), {"sha": "a" * 40, "tree": "b" * 40})
        self.assertEqual([call.args[0] for call in git.call_args_list],
                         [["git", "rev-parse", "HEAD"], ["git", "rev-parse", "HEAD^{tree}"]])

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
            (root / "ci").mkdir(parents=True)
            (root / "ci" / "run.json").write_text(json.dumps({
                "lane": "ready", "targets": ["test:full", "lint:full", "den:test", "build"],
                "started_at": 100.0, "finished_at": 160.0, "status": "failed"
            }))
            summary = root / "summary.md"
            source = {"sha": "a" * 40, "tree": "b" * 40}
            with patch.object(ci, "artifact_root", return_value=root), patch.object(ci, "source", return_value=source), \
                    patch.dict(ci.os.environ, {"GITHUB_STEP_SUMMARY": str(summary)}), \
                    patch("builtins.print") as printed:
                ci.report("failure")
            record = json.loads((root / "ci" / "run.json").read_text())
            self.assertEqual(record["passed_stages"], [])
            self.assertEqual(json.loads((root / "ci" / "source.json").read_text()), source)
            # When a stage inside a batch receipt passes all subjects, passed_stages records it.
            receipts["1-go.json"]["evidence"].append({"stage": "build", "results": {"task": {"status": "passed"}}})
            (batch / "1-go.json").write_text(json.dumps(receipts["1-go.json"]))
            with patch.object(ci, "artifact_root", return_value=root), patch.object(ci, "source", return_value=source), \
                    patch.dict(ci.os.environ, {"GITHUB_STEP_SUMMARY": str(summary)}), \
                    patch("builtins.print"):
                ci.report("failure")
            record = json.loads((root / "ci" / "run.json").read_text())
            self.assertEqual(record["passed_stages"], ["build"])
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

    def test_report_lists_top_ten_packages_closest_to_ceiling_and_notes_unavailable(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            batch = root / "verification" / "batch"
            batch.mkdir(parents=True)
            ceiling = 3584 * ci.MIB
            results = {}
            for index in range(1, 13):
                ratio = round(0.10 + index * 0.07, 4)
                peak = int(ceiling * ratio)
                results[f"example/pkg{index:02d}"] = {
                    "status": "passed",
                    "memory": {
                        "peak_bytes": peak,
                        "limit_bytes": ceiling,
                        "ratio": ratio,
                        "source": "proc_vmhwm" if index == 12 else "wait4_ru_maxrss",
                        "lower_bound": index == 12,
                    },
                }
            results["example/missing1"] = {
                "status": "passed",
                "memory": {"peak_bytes": None, "limit_bytes": ceiling, "ratio": None,
                           "source": "unavailable", "lower_bound": False},
            }
            results["example/missing2"] = {
                "status": "passed",
                "memory": {"peak_bytes": None, "limit_bytes": ceiling, "ratio": None,
                           "source": "unavailable", "lower_bound": False},
            }
            receipt = {"status": "passed", "names": ["test:contract"],
                       "evidence": [{"stage": "test:contract", "results": results}]}
            (batch / "1-go.json").write_text(json.dumps(receipt))
            # A duplicate receipt with a lower peak for pkg12 must not overwrite the higher peak.
            duplicate = {"status": "passed", "names": ["test:contract"],
                         "evidence": [{"stage": "test:contract", "results": {
                             "example/pkg12": {
                                 "status": "passed",
                                 "memory": {"peak_bytes": 100 * ci.MIB, "limit_bytes": ceiling, "ratio": 0.0279,
                                            "source": "wait4_ru_maxrss", "lower_bound": False},
                             }}}]}
            (batch / "2-go.json").write_text(json.dumps(duplicate))
            (batch / "1-go.progress.json").write_text("{}")
            (root / "ci").mkdir(parents=True)
            (root / "ci" / "run.json").write_text(json.dumps({
                "lane": "ready", "targets": ["test:contract"],
                "started_at": 100.0, "finished_at": 160.0, "status": "passed",
            }))
            summary = root / "summary.md"
            source = {"sha": "a" * 40, "tree": "b" * 40}
            with patch.object(ci, "artifact_root", return_value=root), patch.object(ci, "source", return_value=source), \
                    patch.dict(ci.os.environ, {"GITHUB_STEP_SUMMARY": str(summary)}):
                ci.report("success")
            text = summary.read_text()
            self.assertIn("#### Peak package memory", text)
            self.assertIn("`example/pkg12`", text)
            self.assertIn("`proc_vmhwm` (lower bound)", text)
            self.assertIn("`example/pkg03`", text)
            self.assertNotIn("`example/pkg02`", text)
            self.assertNotIn("`example/pkg01`", text)
            self.assertNotIn("`example/missing1`", text)
            self.assertIn("2 packages had unavailable memory observations.", text)
            self.assertLess(text.index("`example/pkg12`"), text.index("`example/pkg11`"))
            self.assertLess(text.index("`example/pkg11`"), text.index("`example/pkg03`"))

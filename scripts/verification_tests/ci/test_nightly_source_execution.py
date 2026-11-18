"""A nightly run binds the one commit and tree every stage verifies."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import zipfile
from unittest.mock import patch

from ci_policy import nightly_evidence as evidence
from ci_policy import nightly_source as source

SOURCE = "a" * 40
CONTROLLER = "b" * 40
TREE = "c" * 40


class NightlySourceTests(unittest.TestCase):
    def select(self, environment, tooling=0):
        with patch.object(source, "commit", side_effect=lambda value: value), \
                patch.object(source.subprocess, "check_output", return_value=TREE + "\n") as git, \
                patch("ci_policy.qualification_source.subprocess.run") as diff:
            diff.return_value.returncode = tooling
            return source.select(environment), git

    def test_scheduled_run_verifies_its_own_commit(self):
        record, git = self.select({"GITHUB_SHA": CONTROLLER, "NIGHTLY_SOURCE": ""})
        self.assertEqual(record, {"sha": CONTROLLER, "tree": TREE})
        git.assert_called_once_with(["git", "rev-parse", CONTROLLER + "^{tree}"], cwd=source.ROOT, text=True)

    def test_dispatch_names_another_commit_with_the_same_tooling(self):
        record, _ = self.select({"GITHUB_SHA": CONTROLLER, "NIGHTLY_SOURCE": SOURCE})
        self.assertEqual(record, {"sha": SOURCE, "tree": TREE})
        with self.assertRaisesRegex(ValueError, "different CI tooling"):
            self.select({"GITHUB_SHA": CONTROLLER, "NIGHTLY_SOURCE": SOURCE}, tooling=1)

    def test_command_reports_the_binding_or_refuses_an_inexact_source(self):
        with tempfile.TemporaryDirectory() as directory:
            output, summary = Path(directory) / "output", Path(directory) / "summary"
            environment = {"GITHUB_OUTPUT": str(output), "GITHUB_STEP_SUMMARY": str(summary)}
            with patch.dict(source.os.environ, environment), \
                    patch.object(source, "select", return_value={"sha": SOURCE, "tree": TREE}):
                self.assertEqual(source.main(), 0)
            self.assertEqual(dict(line.split("=", 1) for line in output.read_text().splitlines()),
                             {"sha": SOURCE, "tree": TREE})
            self.assertIn(SOURCE, summary.read_text())
            for refusal in (ValueError("qualification source must be a full commit SHA"),
                            subprocess.CalledProcessError(128, ["git"])):
                with patch.dict(source.os.environ, environment), patch.object(source, "select", side_effect=refusal), \
                        patch("builtins.print") as said:
                    self.assertEqual(source.main(), 1)
                self.assertIn("::error title=Nightly source::", said.call_args.args[0])
            self.assertEqual(len(output.read_text().splitlines()), 2)


class NightlyEvidenceTests(unittest.TestCase):
    def test_only_a_full_nightly_on_the_exact_source_and_tree_is_reused(self):
        qualification = {"repository": "owner/repo", "source_sha": SOURCE, "source_tree": TREE}
        run = dict(id=43, run_attempt=2, head_sha=CONTROLLER, head_branch="main",
                   display_title=f"Nightly {SOURCE}", event="schedule", conclusion="success")
        exact = evidence.identity("owner/repo", SOURCE, TREE, 43, 2, CONTROLLER)
        cases = (
            ("exact", run, exact, 43),
            ("manual", {**run, "event": "workflow_dispatch"}, exact, 43),
            ("partial", run, None, None),
            ("wrong commit", run, {**exact, "source_sha": CONTROLLER}, None),
            ("wrong tree", run, {**exact, "source_tree": CONTROLLER}, None),
            ("wrong controller", run, {**exact, "controller_sha": SOURCE}, None),
            ("old attempt", run, {**exact, "run_attempt": 1}, None),
            ("branch", {**run, "head_branch": "topic"}, exact, None),
            ("red", {**run, "conclusion": "failure"}, exact, None),
        )
        for label, offered, record, expected in cases:
            with self.subTest(label=label), patch.object(evidence, "pages", return_value=[offered]), \
                    patch.object(evidence, "from_run", return_value=record):
                self.assertEqual(evidence.matching_run(qualification), expected)

    def test_nightly_receipt_refuses_a_checkout_other_than_the_selected_source(self):
        with patch.object(evidence.subprocess, "check_output", side_effect=[CONTROLLER, TREE]), \
                self.assertRaisesRegex(ValueError, "checkout differs"):
            evidence.write({"SOURCE_SHA": SOURCE, "SOURCE_TREE": TREE})

    def test_nightly_receipt_records_the_verified_source_and_its_run(self):
        environment = {"SOURCE_SHA": SOURCE, "SOURCE_TREE": TREE, "GITHUB_REPOSITORY": "owner/repo",
                       "GITHUB_RUN_ID": "43", "GITHUB_RUN_ATTEMPT": "2", "GITHUB_SHA": CONTROLLER}
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(evidence.subprocess, "check_output", side_effect=[SOURCE + "\n", TREE + "\n"]), \
                patch.object(evidence, "Path", side_effect=lambda name: Path(directory) / name):
            evidence.write(environment)
            record = json.loads((Path(directory) / "nightly-source.json").read_text())
        self.assertEqual(record, evidence.identity("owner/repo", SOURCE, TREE, 43, 2, CONTROLLER))

    def test_an_unreadable_nightly_record_is_passed_over_for_the_next_run(self):
        qualification = {"repository": "owner/repo", "source_sha": SOURCE, "source_tree": TREE}
        run = dict(id=43, run_attempt=1, head_sha=CONTROLLER, head_branch="main",
                   display_title=f"Nightly {SOURCE}", event="schedule", conclusion="success")
        later = {**run, "id": 44}
        records = [zipfile.BadZipFile("truncated"), evidence.identity("owner/repo", SOURCE, TREE, 44, 1, CONTROLLER)]
        with patch.object(evidence, "pages", return_value=[run, later]), \
                patch.object(evidence, "from_run", side_effect=records):
            self.assertEqual(evidence.matching_run(qualification), 44)

    def test_selection_reports_reuse_only_for_a_matching_nightly(self):
        qualification = {"version": 1, "repository": "owner/repo", "event": "workflow_dispatch", "run_id": 9,
                         "run_attempt": 1, "source_sha": SOURCE, "source_tree": TREE, "controller_sha": CONTROLLER}
        cases = (("match", {"return_value": 43}, "nightly-run-id=43\n", "Reusing full nightly run 43"),
                 ("none", {"return_value": None}, "", "Verification runs on the candidate"),
                 ("api down", {"side_effect": OSError("unreachable")}, "", "Verification runs on the candidate"))
        for label, lookup, output, summary in cases:
            with self.subTest(label=label), tempfile.TemporaryDirectory() as directory:
                files = Path(directory) / "output", Path(directory) / "summary"
                environment = {"QUALIFICATION_RECORD": json.dumps(qualification),
                               "GITHUB_OUTPUT": str(files[0]), "GITHUB_STEP_SUMMARY": str(files[1])}
                with patch.object(evidence, "matching_run", **lookup):
                    evidence.select(environment)
                self.assertEqual(files[0].read_text() if files[0].exists() else "", output)
                self.assertIn(summary, files[1].read_text())
        unbound = {"QUALIFICATION_RECORD": json.dumps({**qualification, "source_sha": "main"})}
        with self.assertRaisesRegex(ValueError, "bound qualification source"):
            evidence.select(unbound)

    def test_command_runs_the_named_operation(self):
        for operation in ("write", "select"):
            with self.subTest(operation=operation), patch.object(sys, "argv", ["nightly_evidence", operation]), \
                    patch.object(evidence, operation) as chosen:
                with patch.dict(evidence.os.environ, {"MARK": operation}):
                    evidence.main()
                    self.assertEqual(chosen.call_args.args[0]["MARK"], operation)


if __name__ == "__main__":
    unittest.main()

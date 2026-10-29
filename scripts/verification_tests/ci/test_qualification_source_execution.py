"""Selected candidates retain source identity and share one qualification executor."""
import io
import json
import subprocess
from pathlib import Path
import tempfile
import unittest
import zipfile
from unittest.mock import patch

from ci_policy import qualification_receipt as receipt
from ci_policy import qualification_source as source

SOURCE = "a" * 40
CONTROLLER = "b" * 40
TREE = "c" * 40
ENVIRONMENT = dict(GITHUB_REPOSITORY="owner/repo", GITHUB_EVENT_NAME="workflow_dispatch",
                   GITHUB_REF="refs/heads/main", GITHUB_WORKFLOW_SHA=CONTROLLER, GITHUB_SHA=CONTROLLER,
                   GITHUB_RUN_ID="42", GITHUB_RUN_ATTEMPT="2", QUALIFICATION_SOURCE=SOURCE)


def selection():
    return dict(version=1, repository="owner/repo", event="workflow_dispatch", source_sha=SOURCE,
                source_tree=TREE, controller_sha=CONTROLLER, run_id=42, run_attempt=2)


class QualificationSourceTests(unittest.TestCase):
    def choose(self, environment, ancestor=0, head=CONTROLLER):
        with patch.object(source, "commit", side_effect=lambda value: value), \
                patch.object(source, "api", return_value={"object": {"sha": CONTROLLER}}), \
                patch.object(source.subprocess, "check_output", side_effect=[head, TREE]), \
                patch.object(source.subprocess, "run") as command:
            command.return_value.returncode = ancestor
            result = source.select(environment)
            command.assert_any_call(["git", "merge-base", "--is-ancestor", result["source_sha"], CONTROLLER],
                                    cwd=source.ROOT)
            return result

    def test_manual_request_keeps_the_selected_source_after_main_advances(self):
        result = self.choose(ENVIRONMENT)
        self.assertEqual(result, selection())

    def test_default_source_and_commit_identity(self):
        self.assertEqual(self.choose({**ENVIRONMENT, "QUALIFICATION_SOURCE": ""})["source_sha"], CONTROLLER)
        with patch.object(source.subprocess, "check_output", return_value=SOURCE + "\n") as git:
            self.assertEqual(source.commit(SOURCE), SOURCE)
        git.assert_called_once_with(["git", "rev-parse", "--verify", SOURCE + "^{commit}"], cwd=source.ROOT, text=True)
        with patch.object(source.subprocess, "run") as git:
            source.require_compatible_tooling(SOURCE, SOURCE)
        git.assert_not_called()

    def test_source_command_emits_only_the_bound_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            output, summary = Path(directory) / "output", Path(directory) / "summary"
            with patch.dict(source.os.environ, GITHUB_OUTPUT=str(output), GITHUB_STEP_SUMMARY=str(summary)), \
                    patch.object(source, "select", return_value=selection()):
                self.assertEqual(source.main(), 0)
            rows = dict(line.split("=", 1) for line in output.read_text().splitlines())
            self.assertEqual(set(rows), {"source-sha", "record"})
            self.assertEqual(json.loads(rows["record"]), selection())
            self.assertEqual(rows["source-sha"], SOURCE)
            self.assertIn(CONTROLLER, summary.read_text())

    def test_selection_rejects_foreign_controllers_and_sources_outside_main(self):
        for environment in ({**ENVIRONMENT, "GITHUB_REF": "refs/heads/feature"},
                            {**ENVIRONMENT, "GITHUB_EVENT_NAME": "pull_request"},
                            {**ENVIRONMENT, "GITHUB_EVENT_NAME": "push"}):
            with self.subTest(environment=environment), self.assertRaises(ValueError):
                source.select(environment)
        with self.assertRaisesRegex(ValueError, "ancestor"):
            self.choose(ENVIRONMENT, ancestor=1)
        with self.assertRaisesRegex(ValueError, "controller checkout"):
            self.choose(ENVIRONMENT, head=SOURCE)
        for value in ("main", SOURCE[:12], "-x", None):
            with self.subTest(value=value), patch.object(source.subprocess, "check_output") as git, \
                    self.assertRaises(ValueError):
                source.commit(value)
            git.assert_not_called()

    def test_incompatible_tooling_refuses_before_candidate_execution(self):
        for code in (1, 128):
            result = subprocess.CompletedProcess([], code)
            with self.subTest(code=code), patch.object(source.subprocess, "run", return_value=result) as run:
                with self.assertRaises((ValueError, subprocess.CalledProcessError)):
                    source.require_compatible_tooling(SOURCE, CONTROLLER)
                self.assertEqual(run.call_args.args[0], ["git", "diff", "--quiet", SOURCE, CONTROLLER, "--",
                                                       ".github", "scripts", "task", "Taskfile.yml"])
        with patch.object(source.subprocess, "run", return_value=subprocess.CompletedProcess([], 0)):
            source.require_compatible_tooling(SOURCE, CONTROLLER)
        with patch.object(source, "require_compatible_tooling", side_effect=ValueError("tooling differs")) as check:
            with self.assertRaisesRegex(ValueError, "tooling differs"):
                self.choose(ENVIRONMENT)
            check.assert_called_once_with(SOURCE, CONTROLLER)


class QualificationReceiptTests(unittest.TestCase):
    def test_only_a_complete_dispatch_attempt_produces_a_receipt(self):
        results = {name: {"result": "success"} for name in receipt.CHECKS}
        completed = receipt.complete(selection(), results, ENVIRONMENT)
        self.assertEqual(completed["source_sha"], SOURCE)
        self.assertEqual(completed["controller_sha"], CONTROLLER)
        for changed in ({"event": "push"}, {"run_attempt": 3}, {"source_sha": "main"},
                        {"controller_sha": SOURCE}, {"repository": "foreign/repo"}, {"version": True}):
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                receipt.complete({**selection(), **changed}, results, ENVIRONMENT)
        for name in receipt.CHECKS:
            for status in ("failure", "cancelled", "skipped"):
                with self.subTest(name=name, status=status), self.assertRaises(ValueError):
                    receipt.complete(selection(), {**results, name: {"result": status}}, ENVIRONMENT)
        for malformed in ({}, {**results, "other": {"result": "success"}}):
            with self.assertRaises(ValueError):
                receipt.complete(selection(), malformed, ENVIRONMENT)

    def test_failed_job_rerun_binds_retained_selection_to_the_completed_attempt(self):
        results = {name: {"result": "success"} for name in receipt.CHECKS}
        completed = receipt.complete({**selection(), "run_attempt": 1}, results, ENVIRONMENT)
        self.assertEqual(completed["run_attempt"], 2)
        self.assertEqual(completed["source_sha"], SOURCE)
        for changed in ({"run_id": 7}, {"controller_sha": SOURCE}, {"run_attempt": 3}):
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                receipt.complete({**selection(), **changed}, results, ENVIRONMENT)

    def test_receipt_binds_source_controller_repository_and_attempt(self):
        results = {name: {"result": "success"} for name in receipt.CHECKS}
        completed = receipt.complete(selection(), results, ENVIRONMENT)
        run = dict(id=42, run_attempt=2, head_sha=CONTROLLER, event="workflow_dispatch")
        self.assertTrue(receipt.matches(completed, "owner/repo", SOURCE, run))
        for changed in ({"source_sha": CONTROLLER}, {"controller_sha": SOURCE}, {"run_id": 7},
                        {"run_attempt": 1}, {"repository": "foreign/repo"}, {"event": "push"}, {"checks": {}}):
            with self.subTest(changed=changed):
                self.assertFalse(receipt.matches({**completed, **changed}, "owner/repo", SOURCE, run))

    def test_archive_parsing_is_bounded_and_does_not_extract_paths(self):
        for name, payload, valid in [("qualification-source.json", json.dumps(selection()), True),
                                     ("../qualification-source.json", "{}", False),
                                     ("qualification-source.json", "x" * (receipt.MAX_BYTES + 1), False)]:
            data = io.BytesIO()
            with zipfile.ZipFile(data, "w", zipfile.ZIP_DEFLATED) as archive:
                archive.writestr(name, payload)
            with self.subTest(name=name, valid=valid):
                if valid:
                    self.assertEqual(receipt.decode(data.getvalue()), selection())
                else:
                    with self.assertRaises(ValueError):
                        receipt.decode(data.getvalue())

    def test_receipt_lookup_selects_only_one_unexpired_current_attempt(self):
        run = dict(id=42, run_attempt=2)
        selected = dict(id=7, name="qualification-source-2", expired=False, size_in_bytes=100)
        old = dict(id=6, name="qualification-source-1", expired=False, size_in_bytes=100)
        with patch.object(receipt, "pages", return_value=[old, selected]), \
                patch.object(receipt.subprocess, "check_output", return_value=b"archive") as download, \
                patch.object(receipt, "decode", return_value=selection()):
            self.assertEqual(receipt.from_run("owner/repo", run), selection())
            download.assert_called_once_with(["gh", "api", "repos/owner/repo/actions/artifacts/7/zip"])
        for rows in ([old], [{**selected, "expired": True}]):
            with patch.object(receipt, "pages", return_value=rows), \
                    patch.object(receipt.subprocess, "check_output") as download:
                self.assertIsNone(receipt.from_run("owner/repo", run))
                download.assert_not_called()
        for rows in ([selected, selected], [{**selected, "size_in_bytes": receipt.MAX_BYTES + 1}]):
            with patch.object(receipt, "pages", return_value=rows), \
                    patch.object(receipt.subprocess, "check_output") as download, self.assertRaises(ValueError):
                receipt.from_run("owner/repo", run)
            download.assert_not_called()

    def test_aggregate_lookup_requires_a_unique_success_on_the_completed_attempt(self):
        run = dict(id=42, run_attempt=2)
        good = dict(name="qualification", conclusion="success")
        for jobs, accepted in [([good], True), ([], False), ([good, good], False),
                               ([{**good, "conclusion": "skipped"}], False)]:
            with self.subTest(jobs=jobs), patch.object(receipt, "pages", return_value=jobs) as pages:
                self.assertEqual(receipt.aggregate_passed("owner/repo", run), accepted)
                pages.assert_called_once_with("repos/owner/repo/actions/runs/42/attempts/2/jobs", "jobs")

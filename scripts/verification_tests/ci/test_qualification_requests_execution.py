"""Selected candidates retain source identity and share one qualification executor."""
import io
import json
import unittest
import zipfile
from unittest.mock import patch

from ci_policy import qualification_receipt as receipt
from ci_policy import qualification_requests as requests

SOURCE = "a" * 40
CONTROLLER = "b" * 40
TREE = "c" * 40
ENVIRONMENT = dict(GITHUB_REPOSITORY="owner/repo", GITHUB_EVENT_NAME="workflow_dispatch",
                   GITHUB_REF="refs/heads/main", GITHUB_WORKFLOW_SHA=CONTROLLER, GITHUB_SHA=CONTROLLER,
                   GITHUB_RUN_ID="42", GITHUB_RUN_ATTEMPT="2", QUALIFICATION_SOURCE=SOURCE)


def selection():
    return dict(version=1, repository="owner/repo", event="workflow_dispatch", source_sha=SOURCE,
                source_tree=TREE, controller_sha=CONTROLLER, run_id=42, run_attempt=2,
                admitted=True, reason="selected candidate")


class QualificationRequestTests(unittest.TestCase):
    def choose(self, environment, ancestor=0, head=CONTROLLER):
        with patch.object(requests, "commit", side_effect=lambda value: value), \
                patch.object(requests, "api", return_value={"object": {"sha": CONTROLLER}}), \
                patch.object(requests.subprocess, "check_output", side_effect=[head, TREE]), \
                patch.object(requests.subprocess, "run") as command:
            command.return_value.returncode = ancestor
            result = requests.select(environment)
            command.assert_any_call(["git", "merge-base", "--is-ancestor", result["source_sha"], CONTROLLER],
                                    cwd=requests.ROOT)
            return result

    def test_manual_request_keeps_the_selected_source_after_main_advances(self):
        result = self.choose(ENVIRONMENT)
        self.assertEqual(result, selection())

    def test_background_skips_superseded_sources_and_preserves_current_main(self):
        background = {**ENVIRONMENT, "GITHUB_EVENT_NAME": "push", "QUALIFICATION_SOURCE": "", "GITHUB_SHA": SOURCE}
        result = self.choose(background)
        self.assertFalse(result["admitted"])
        self.assertEqual(result["reason"], "superseded main snapshot")
        background["GITHUB_SHA"] = CONTROLLER
        self.assertTrue(self.choose(background)["admitted"])

    def test_selection_rejects_foreign_controllers_and_sources_outside_main(self):
        for environment in ({**ENVIRONMENT, "GITHUB_REF": "refs/heads/feature"},
                            {**ENVIRONMENT, "GITHUB_EVENT_NAME": "pull_request"}):
            with self.subTest(environment=environment), self.assertRaises(ValueError):
                requests.select(environment)
        with self.assertRaisesRegex(ValueError, "ancestor"):
            self.choose(ENVIRONMENT, ancestor=1)
        with self.assertRaisesRegex(ValueError, "controller checkout"):
            self.choose(ENVIRONMENT, head=SOURCE)
        for value in ("main", SOURCE[:12], "-x", None):
            with self.subTest(value=value), patch.object(requests.subprocess, "check_output") as git, \
                    self.assertRaises(ValueError):
                requests.commit(value)
            git.assert_not_called()


class QualificationReceiptTests(unittest.TestCase):
    def test_only_a_complete_admitted_attempt_produces_a_receipt(self):
        results = {name: {"result": "success"} for name in receipt.CHECKS}
        completed = receipt.complete(selection(), results, ENVIRONMENT)
        self.assertEqual(completed["source_sha"], SOURCE)
        self.assertEqual(completed["controller_sha"], CONTROLLER)
        self.assertNotIn("reason", completed)
        for changed in ({"admitted": False}, {"run_attempt": 1}, {"source_sha": "main"},
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

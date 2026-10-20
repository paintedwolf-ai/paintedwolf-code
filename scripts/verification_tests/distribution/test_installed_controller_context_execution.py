"""Both producer and publisher require the immutable protected controller context."""

import unittest

from linux_release.installed_provenance import controller_context


class PublicationContextTests(unittest.TestCase):
    def environment(self):
        return {"GITHUB_REPOSITORY": "owner/repo", "GITHUB_REPOSITORY_ID": "42",
                "GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_REF": "refs/heads/main",
                "GITHUB_WORKFLOW_REF": "owner/repo/.github/workflows/release.yml@refs/heads/main",
                "GITHUB_SHA": "a" * 40, "GITHUB_WORKFLOW_SHA": "a" * 40,
                "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "2"}

    def test_exact_main_controller_revision_and_attempt_are_retained(self):
        self.assertEqual(controller_context(self.environment()), {"repository_id": 42,
            "run_id": 123, "run_attempt": 2, "controller_commit": "a" * 40})

    def test_other_actor_revision_workflow_or_invalid_id_cannot_authorize(self):
        for key, value in [("GITHUB_EVENT_NAME", "pull_request"), ("GITHUB_REF", "refs/tags/v1"),
                           ("GITHUB_WORKFLOW_REF", "owner/repo/.github/workflows/other.yml@refs/heads/main"),
                           ("GITHUB_WORKFLOW_SHA", "b" * 40), ("GITHUB_SHA", "invalid"),
                           ("GITHUB_REPOSITORY_ID", "0"), ("GITHUB_RUN_ID", "-1"),
                           ("GITHUB_RUN_ATTEMPT", "True")]:
            with self.subTest(key=key):
                environment = self.environment()
                environment[key] = value
                with self.assertRaises(ValueError):
                    controller_context(environment)

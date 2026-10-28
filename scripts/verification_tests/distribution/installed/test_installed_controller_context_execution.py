"""Local execution and protected release attempts retain distinct identities."""

import unittest

from linux_release.campaign.provenance import local_controller_context


class LocalControllerContextTests(unittest.TestCase):
    def test_exact_local_controller_is_retained(self):
        self.assertEqual(local_controller_context("a" * 40, 42, "qualification-host"),
                         {"execution": "local", "repository_id": 42, "controller_commit": "a" * 40,
                          "host": "qualification-host"})

    def test_invalid_commit_repository_or_host_cannot_authorize(self):
        for commit, repository, host in [("invalid", 42, "host"), ("A" * 40, 42, "host"), ("a" * 40, 0, "host"),
                                         ("a" * 40, True, "host"), ("a" * 40, 42, ""), ("a" * 40, 42, "bad host/name")]:
            with self.subTest(commit=commit, repository=repository, host=host), self.assertRaises(ValueError):
                local_controller_context(commit, repository, host)


class ProtectedControllerContextTests(unittest.TestCase):
    def environment(self):
        return {"GITHUB_REPOSITORY": "owner/repo", "GITHUB_SHA": "a" * 40,
                "GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_REF": "refs/heads/main",
                "GITHUB_WORKFLOW_REF": "owner/repo/.github/workflows/release.yml@refs/heads/main",
                "GITHUB_WORKFLOW_SHA": "a" * 40, "GITHUB_REPOSITORY_ID": "42",
                "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "2"}

    def test_protected_attempt_has_no_local_host_authority(self):
        from linux_release.campaign.provenance import protected_controller_context
        self.assertEqual(protected_controller_context(self.environment()),
                         {"repository_id": 42, "run_id": 123, "run_attempt": 2, "controller_commit": "a" * 40})
        with self.assertRaises(ValueError):
            protected_controller_context(local_controller_context("a" * 40, 42, "host"))

    def test_nonmain_or_incomplete_producer_is_refused(self):
        from linux_release.campaign.provenance import protected_controller_context
        for field, value in (("GITHUB_REF", "refs/heads/feature"), ("GITHUB_EVENT_NAME", "pull_request"),
                ("GITHUB_WORKFLOW_REF", "owner/repo/.github/workflows/other.yml@refs/heads/main"),
                ("GITHUB_WORKFLOW_SHA", "b" * 40), ("GITHUB_RUN_ID", "0"),
                ("GITHUB_REPOSITORY_ID", True), ("GITHUB_RUN_ATTEMPT", "")):
            with self.subTest(field=field), self.assertRaises(ValueError):
                protected_controller_context({**self.environment(), field: value})

    def test_missing_or_malformed_environment_cannot_establish_an_attempt(self):
        from linux_release.campaign.provenance import protected_controller_context
        environment = self.environment()
        for field in environment:
            for value in (None, True, ""):
                with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                    protected_controller_context({**environment, field: value})

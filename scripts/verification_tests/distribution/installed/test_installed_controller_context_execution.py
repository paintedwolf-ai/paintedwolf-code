"""The local qualification controller is an exact commit, repository and host."""

import unittest

from linux_release.installed_provenance import controller_context


class LocalControllerContextTests(unittest.TestCase):
    def test_exact_local_controller_is_retained(self):
        self.assertEqual(controller_context("a" * 40, 42, "qualification-host"),
                         {"execution": "local", "repository_id": 42, "controller_commit": "a" * 40,
                          "host": "qualification-host"})

    def test_invalid_commit_repository_or_host_cannot_authorize(self):
        for commit, repository, host in [("invalid", 42, "host"), ("A" * 40, 42, "host"), ("a" * 40, 0, "host"),
                                         ("a" * 40, True, "host"), ("a" * 40, 42, ""), ("a" * 40, 42, "bad host/name")]:
            with self.subTest(commit=commit, repository=repository, host=host), self.assertRaises(ValueError):
                controller_context(commit, repository, host)

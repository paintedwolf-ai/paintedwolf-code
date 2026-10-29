"""Pinned Go input provisioning retries a failed fetch without hiding its failure."""

import subprocess
import unittest
from unittest.mock import patch

from ci_policy import go_retry


class GoRetryTests(unittest.TestCase):
    def test_failed_download_retries_then_succeeds(self):
        command = ["go", "mod", "download"]
        failure = subprocess.CalledProcessError(1, command)
        with patch.object(go_retry.subprocess, "run", side_effect=[failure, failure, subprocess.CompletedProcess(command, 0)]) as run, \
                patch.object(go_retry.time, "sleep") as sleep:
            go_retry.run_go(command, cwd="/tmp/module")
        self.assertEqual(run.call_count, 3)
        run.assert_any_call(command, cwd="/tmp/module", env=None, check=True)
        self.assertEqual(sleep.call_count, 2)

    def test_permanent_failure_is_returned_after_bounded_attempts(self):
        command = ["go", "install", "example.com/tool@v1.0.0"]
        failure = subprocess.CalledProcessError(42, command)
        with patch.object(go_retry.subprocess, "run", side_effect=failure) as run, \
                patch.object(go_retry.time, "sleep") as sleep:
            with self.assertRaises(subprocess.CalledProcessError) as raised:
                go_retry.run_go(command, cwd="/tmp/module", attempts=3)
        self.assertEqual(raised.exception.returncode, 42)
        self.assertEqual(run.call_count, 3)
        self.assertEqual(sleep.call_count, 2)

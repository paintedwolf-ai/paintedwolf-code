"""Decision dependency preparation preserves locked inputs and stops on failure."""
import subprocess
import unittest
from unittest.mock import patch

import decide_prepare


class DecidePreparationTests(unittest.TestCase):
    def test_default_fetch_cannot_change_lockfile(self):
        with patch.object(decide_prepare.subprocess, "run") as run:
            decide_prepare.prepare()
        self.assertEqual(run.call_count, 1)
        self.assertEqual(run.call_args.args[0][:3], ["cargo", "fetch", "--locked"])
        self.assertTrue(run.call_args.kwargs["check"])

    def test_refresh_preserves_dependency_versions_before_locked_fetch(self):
        with patch.object(decide_prepare.subprocess, "run") as run:
            decide_prepare.prepare(True)
        self.assertEqual([call.args[0][:3] for call in run.call_args_list],
                         [["cargo", "update", "--workspace"], ["cargo", "fetch", "--locked"]])

    def test_failed_resolution_prevents_fetch(self):
        failure = subprocess.CalledProcessError(101, ["cargo", "update"])
        with patch.object(decide_prepare.subprocess, "run", side_effect=failure) as run:
            with self.assertRaises(subprocess.CalledProcessError):
                decide_prepare.prepare(True)
        self.assertEqual(run.call_count, 1)

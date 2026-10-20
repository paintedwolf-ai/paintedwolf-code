"""Process cleanup preserves foreign PIDs and retires supervision before engines."""
from pathlib import Path
import signal
import unittest
from unittest.mock import patch

import release_upgrade.process as process


class ProcessTests(unittest.TestCase):
    def installation(self):
        installation = process.Installation.__new__(process.Installation)
        installation.app = Path("/test/Product.app")
        installation.config = Path("/test/config")
        installation.executable = installation.app / "Contents/MacOS/Product"
        return installation

    def test_cleanup_orders_owned_processes_and_does_not_signal_a_reused_pid(self):
        installation = self.installation()
        installation.owned = {10: ("app", 1), 11: ("engine", 2), 12: ("helper", 0), 13: ("old identity", 1)}
        identities = {10: "app", 11: "engine", 12: "helper", 13: "foreign process"}
        signals = []
        def kill(pid, sig):
            signals.append((pid, sig))
            identities.pop(pid)
        with patch.object(installation, "capture_owned"), \
             patch.object(process, "process_identity", side_effect=lambda pid: identities.get(pid)), \
             patch.object(process.os, "kill", side_effect=kill):
            installation.stop()
        self.assertEqual(signals, [(12, signal.SIGTERM), (10, signal.SIGTERM), (11, signal.SIGTERM)])
        self.assertEqual(identities, {13: "foreign process"})

    def test_wait_timeout_is_bounded_and_reports_the_missing_transition(self):
        with patch.object(process.time, "monotonic", side_effect=[0, 1]), \
             patch.object(process.time, "sleep") as sleep:
            with self.assertRaisesRegex(TimeoutError, "startup"):
                process.wait_for("startup", lambda: None, seconds=0.5)
            sleep.assert_not_called()

    def test_launch_supplies_the_model_mock_without_replacing_the_account_home(self):
        installation = self.installation()
        with patch.object(process.subprocess, "run") as run:
            installation.launch()
        self.assertEqual(run.call_args.args[0], ["/usr/bin/open", "-n", "-j", "--env", "LYCAON_LLM_MOCK=1", str(installation.app)])

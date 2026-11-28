"""Exercise cleanup with an actual child PID and runner signals."""

import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest


RUNNER = """
from pathlib import Path
import sys
import time
from unittest.mock import patch

from grounding.sidecar_driver import IsolatedSidecarProcess, SidecarSessionClient

root = Path(sys.argv[1])
mode = sys.argv[2]

def health(_client):
    if mode in {"startup", "error"}:
        (root / "interrupt-here").touch()
        if mode == "error":
            raise ValueError("readiness failed")
        while True:
            time.sleep(1)

with patch.multiple(SidecarSessionClient, health=health,
                    disable_auto_llm=lambda _: None, probe_manual_boundary=lambda _: None):
    with IsolatedSidecarProcess(root / "lycaon-dev", product_root=root / "product",
                                config_dir=root / "config", workspace_dir=root / "workspace"):
        (root / "interrupt-here").touch()
        while True:
            time.sleep(1)
"""


class TestSidecarLifecycle(unittest.TestCase):
    def test_runner_interruption_reaps_owned_sidecar(self):
        for mode, signum in (("startup", signal.SIGTERM), ("active", signal.SIGINT), ("error", None)):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                self.check_runner(Path(directory), mode, signum)

    def check_runner(self, root, mode, signum):
        (root / "product").mkdir()
        executable = root / "lycaon-dev"
        executable.write_text("#!/bin/sh\nexec sleep 60\n", encoding="utf-8")
        executable.chmod(0o700)

        benchmark = Path(__file__).resolve().parents[1]
        runner = subprocess.Popen(
            [sys.executable, "-c", RUNNER, str(root), mode],
            cwd=benchmark, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
        pid = None
        try:
            deadline = time.monotonic() + 15
            while not (root / "interrupt-here").exists() and runner.poll() is None:
                self.assertLess(time.monotonic(), deadline, "runner did not reach interruption point")
                time.sleep(0.02)
            ownership = root / "config" / "ownership.json"
            pid = json.loads(ownership.read_text(encoding="utf-8"))["pid"]
            if signum is not None:
                os.kill(pid, 0)
                runner.send_signal(signum)
            _, errors = runner.communicate(timeout=15)
            expected = 1 if signum is None else 128 + signum
            self.assertEqual(runner.returncode, expected, errors)
            if signum is None:
                self.assertIn("ValueError: readiness failed", errors)
            with self.assertRaises(ProcessLookupError):
                os.kill(pid, 0)
            pid = None
        finally:
            if runner.poll() is None:
                runner.kill()
                runner.communicate(timeout=5)
            if pid is not None:
                try:
                    os.kill(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass

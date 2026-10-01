"""Exercise harness shutdown with real processes and isolated TCP listeners."""

from __future__ import annotations

import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import Mock, patch



HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
from harness import supervise
WORKER = '''import json, os, signal, socket, subprocess, sys, time
from pathlib import Path
if len(sys.argv) > 2:
    subprocess.Popen([sys.executable, __file__, sys.argv[2]])
if os.environ.get("IGNORE_TERM") == "1":
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
server = socket.socket()
server.bind(("127.0.0.1", 0))
server.listen()
Path(sys.argv[1]).write_text(json.dumps({"pid": os.getpid(), "port": server.getsockname()[1]}))
while True:
    time.sleep(1)
'''
RUNNER = '''#!/usr/bin/env bash
set -euo pipefail
source "$HARNESS_LIB"
mkdir -p "$LYCAON_E2E_STATE_DIR"
harness_claim_state "$LYCAON_E2E_STATE_DIR"
mkdir -p "$LYCAON_E2E_STATE_DIR/snapshot"
echo data > "$LYCAON_E2E_STATE_DIR/snapshot/source"
chmod 500 "$LYCAON_E2E_STATE_DIR/snapshot"
nohup "$TEST_PYTHON" "$PROBE/worker.py" "$PROBE/sidecar.json" >/dev/null 2>&1 &
"$TEST_PYTHON" "$PROBE/worker.py" "$PROBE/vite.json" "$PROBE/grandchild.json" &
echo "$$" > "$PROBE/runner.pid"
while [[ ! -f "$PROBE/finish" ]]; do sleep 0.1; done
exit 7
'''


class HarnessSignalTest(unittest.TestCase):
    def test_permission_denied_probe_still_means_group_exists(self):
        with patch.object(supervise.os, "killpg", side_effect=PermissionError):
            self.assertTrue(supervise.signal_group(123, 0))

    def test_live_group_signal_denial_is_not_hidden(self):
        with patch.object(supervise.os, "killpg", side_effect=PermissionError), patch.object(
            supervise.subprocess, "check_output", return_value="123 S\n"
        ):
            with self.assertRaises(PermissionError):
                supervise.signal_group(123, signal.SIGTERM)

    def test_zombie_or_absent_group_needs_no_signal(self):
        for rows in ("123 Z\n", "456 S\n", ""):
            with self.subTest(rows=rows), patch.object(supervise.os, "killpg", side_effect=PermissionError), patch.object(
                supervise.subprocess, "check_output", return_value=rows
            ):
                self.assertFalse(supervise.signal_group(123, signal.SIGKILL))

    def test_cleanup_runs_even_when_signalling_fails(self):
        with tempfile.TemporaryDirectory() as state, patch.dict(os.environ, {"LYCAON_E2E_STATE_DIR": state}), patch.object(
            supervise.sys, "argv", ["supervise.py", "fixture.sh"]
        ), patch.object(supervise.signal, "signal"), patch.object(
            supervise.subprocess, "Popen", return_value=Mock(poll=Mock(return_value=7))
        ), patch.object(supervise, "stop_stack", side_effect=PermissionError), patch.object(
            supervise, "release_state", return_value=0
        ) as release:
            with self.assertRaises(PermissionError):
                supervise.main()
            release.assert_called_once()

    def test_shutdown_waits_through_a_denied_probe(self):
        child = Mock(pid=123)
        with patch.object(supervise.os, "killpg", side_effect=[None, PermissionError(), ProcessLookupError(), ProcessLookupError()]), patch.object(supervise.time, "sleep"):
            supervise.stop_stack(child)
        child.poll.assert_called_once()
        child.wait.assert_called_once()


class HarnessSupervisorTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory(prefix="harness-lifecycle-test.")
        self.root = Path(self.temp.name)
        self.state = self.root / "state with spaces"
        (self.root / "worker.py").write_text(WORKER)
        (self.root / "runner.sh").write_text(RUNNER)
        self.log = (self.root / "supervisor.log").open("w+")
        self.children = []
        self.env = os.environ.copy()
        self.env.update(PROBE=str(self.root), HARNESS_LIB=str(HERE / "lib.sh"),
                        TEST_PYTHON=sys.executable, LYCAON_E2E_STATE_DIR=str(self.state),
                        LYCAON_E2E_ADDR="127.0.0.1:1", LYCAON_E2E_VITE_PORT="2",
                        LYCAON_HARNESS_TIMEOUT_SECONDS="30", HARNESS_KEEP_STATE="0")
        self.env.pop("HARNESS_SUPERVISOR_PID", None)
        self.env.pop("HARNESS_SUPERVISOR_TOKEN", None)

    def tearDown(self) -> None:
        for child in self.children:
            if child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait()
        for marker in self.root.glob("*.json"):
            try:
                os.kill(json.loads(marker.read_text())["pid"], signal.SIGKILL)
            except (ProcessLookupError, json.JSONDecodeError):
                pass
        snapshot = self.state / "snapshot"
        if snapshot.exists():
            snapshot.chmod(0o700)
        self.log.close()
        self.temp.cleanup()

    def launch(self, **env: str) -> subprocess.Popen:
        self.env.update(env)
        child = subprocess.Popen(
            [sys.executable, str(HERE / "supervise.py"), str(self.root / "runner.sh")],
            env=self.env, stdout=self.log, stderr=self.log,
        )
        self.children.append(child)
        return child

    def await_ready(self) -> None:
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if all((self.root / name).exists() for name in (
                "runner.pid", "sidecar.json", "vite.json", "grandchild.json",
            )):
                return
            time.sleep(0.05)
        self.fail("fixture did not become ready: " + (self.root / "supervisor.log").read_text())

    def assert_stopped(self, child: subprocess.Popen, code: int) -> None:
        self.assertEqual(child.wait(timeout=15), code, (self.root / "supervisor.log").read_text())
        for marker in self.root.glob("*.json"):
            port = json.loads(marker.read_text())["port"]
            with socket.socket() as probe:
                self.assertNotEqual(probe.connect_ex(("127.0.0.1", port)), 0,
                                    str(marker) + "\n" + (self.root / "supervisor.log").read_text())

    def test_completion_cleans_descendants_and_preserves_peer_listener(self) -> None:
        with socket.socket() as peer:
            peer.bind(("127.0.0.1", 0))
            peer.listen()
            port = peer.getsockname()[1]
            child = self.launch(LYCAON_E2E_ADDR=f"127.0.0.1:{port}")
            self.await_ready()
            (self.root / "finish").touch()
            self.assert_stopped(child, 7)
            self.assertFalse(self.state.exists())
            with socket.create_connection(("127.0.0.1", port), timeout=1):
                pass

    def test_signals_clean_stack(self) -> None:
        for sig in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
            with self.subTest(signal=sig):
                for marker in self.root.glob("*.json"):
                    marker.unlink()
                (self.root / "runner.pid").unlink(missing_ok=True)
                child = self.launch()
                self.await_ready()
                child.send_signal(sig)
                self.assert_stopped(child, 128 + sig)
                self.assertFalse(self.state.exists())

    def test_killed_runner_still_cleans_nohup_and_grandchildren(self) -> None:
        child = self.launch()
        self.await_ready()
        os.kill(int((self.root / "runner.pid").read_text()), signal.SIGKILL)
        self.assert_stopped(child, 137)
        self.assertFalse(self.state.exists())

    def test_timeout_escalates_uncooperative_children(self) -> None:
        child = self.launch(LYCAON_HARNESS_TIMEOUT_SECONDS="3", IGNORE_TERM="1")
        self.await_ready()
        self.assert_stopped(child, 124)
        self.assertFalse(self.state.exists())

    def test_keep_state_releases_lease_after_stopping(self) -> None:
        child = self.launch(HARNESS_KEEP_STATE="1")
        self.await_ready()
        child.terminate()
        self.assert_stopped(child, 143)
        self.assertEqual((self.state / "snapshot/source").read_text(), "data\n")
        self.assertFalse((self.state / ".harness-lease").exists())

    def test_replacement_sidecar_outside_group_is_stopped(self) -> None:
        child = self.launch()
        self.await_ready()
        replacement = subprocess.Popen(
            [sys.executable, str(self.root / "worker.py"), str(self.root / "replacement.json")],
            env=self.env, stdout=self.log, stderr=self.log, start_new_session=True,
        )
        self.children.append(replacement)
        started = subprocess.check_output(
            ["ps", "-p", str(replacement.pid), "-o", "lstart="], text=True,
        ).strip()
        (self.state / "sidecar.pid").write_text(str(replacement.pid))
        (self.state / "sidecar.started").write_text(started + "\n")
        child.terminate()
        self.assert_stopped(child, 143)
        self.assertIsNotNone(replacement.poll())
        self.assertFalse(self.state.exists())

    def test_stale_sidecar_identity_does_not_stop_foreign_process(self) -> None:
        self.state.mkdir()
        (self.state / "sidecar.pid").write_text(str(os.getpid()))
        (self.state / "sidecar.started").write_text("not this process start time\n")
        stop = subprocess.run(
            ["bash", str(HERE.parent / "e2e-sidecar-stop.sh")], env=self.env,
            stdout=self.log, stderr=self.log, check=False,
        )
        self.assertEqual(stop.returncode, 1)
        self.assertTrue((self.state / "sidecar.pid").exists())

    def test_live_foreign_lease_survives_startup_failure(self) -> None:
        self.state.mkdir()
        marker = self.state / ".harness-lease"
        lease = f"{os.getpid()}\nforeign-token\n"
        marker.write_text(lease)
        child = self.launch()
        self.assert_stopped(child, 1)
        self.assertEqual(marker.read_text(), lease)

    def test_launcher_exit_cleans_stack(self) -> None:
        launcher = '''import subprocess, sys, time
from pathlib import Path
child = subprocess.Popen(sys.argv[1:])
Path("supervisor.pid").write_text(str(child.pid))
while True: time.sleep(1)
'''
        child = subprocess.Popen(
            [sys.executable, "-c", launcher, sys.executable, str(HERE / "supervise.py"),
             str(self.root / "runner.sh")], cwd=self.root, env=self.env,
            stdout=self.log, stderr=self.log,
        )
        self.children.append(child)
        self.await_ready()
        child.kill()
        child.wait()
        deadline = time.monotonic() + 15
        while self.state.exists() and time.monotonic() < deadline:
            time.sleep(0.1)
        self.assertFalse(self.state.exists(), "orphaned supervisor did not release state")
        self.assert_stopped(child, -signal.SIGKILL)


if __name__ == "__main__":
    unittest.main()

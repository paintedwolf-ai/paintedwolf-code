"""Development stop selects real fixture processes only by locks or listeners."""

import fcntl
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import unittest


class DevelopmentStopTests(unittest.TestCase):
    def setUp(self):
        self.assertIsNotNone(shutil.which("lsof"), "development stop fixtures require lsof")
        self.temp = tempfile.TemporaryDirectory(prefix="development-stop.")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.engine = self.bin / "pw"
        shutil.copyfile(shutil.which("sleep"), self.engine)
        self.engine.chmod(0o700)
        self.config = self.root / "dev"
        (self.config / "locks").mkdir(parents=True)
        self.env = dict(os.environ, LYCAON_CONFIG_DIR=str(self.config), LYCAON_ADDR="127.0.0.1:0",
                        PATH=str(self.bin) + os.pathsep + os.environ["PATH"])
        self.script = Path(__file__).resolve().parents[1] / "den-sidecar-stop.sh"
        self.processes = []
        self.addCleanup(self.stop_fixtures)

    def start(self, *, engine=True, lock=None, listener=False):
        held = []
        if lock is not None:
            lock.parent.mkdir(parents=True, exist_ok=True)
            stream = lock.open("a+")
            fcntl.flock(stream.fileno(), fcntl.LOCK_EX)
            held.append(stream)
        port = None
        if listener:
            server = socket.socket()
            server.bind(("127.0.0.1", 0))
            server.listen()
            port = server.getsockname()[1]
            held.append(server)
        args = [str(self.engine), "900"] if engine else [sys.executable, "-c", "import time; time.sleep(900)"]
        child = subprocess.Popen(args, pass_fds=tuple(item.fileno() for item in held))
        self.processes.append(child)
        for item in held:
            item.close()
        threading.Thread(target=child.wait, daemon=True).start()
        return child, port

    def stop_fixtures(self):
        for child in self.processes:
            if child.poll() is None:
                child.terminate()
            child.wait(timeout=10)

    def restrict_name_lookup_to(self, child):
        # A regression cannot discover or signal an actual application engine.
        lookup = self.bin / "pgrep"
        lookup.write_text(f'#!/bin/sh\nprintf "%s\\n" "{child.pid}"\n')
        lookup.chmod(0o700)

    def stop_selected(self, port=0):
        return subprocess.run(["bash", str(self.script)], env={**self.env, "LYCAON_ADDR": f"127.0.0.1:{port}"},
                              text=True, capture_output=True, timeout=60)

    def test_dev_lock_stops_only_its_engine(self):
        dev, _ = self.start(lock=self.config / "locks/dev.engine.lock")
        installed, _ = self.start(lock=self.root / "installed/locks/release.engine.lock")
        self.restrict_name_lookup_to(installed)
        result = self.stop_selected()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIsNotNone(dev.poll(), result.stderr)
        self.assertIsNone(installed.poll(), result.stderr)

    def test_selected_address_stops_only_its_listener(self):
        dev, port = self.start(listener=True)
        installed, _ = self.start()
        self.restrict_name_lookup_to(installed)
        result = self.stop_selected(port)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIsNotNone(dev.poll(), result.stderr)
        self.assertIsNone(installed.poll(), result.stderr)

    def test_foreign_lock_holder_is_preserved_and_reported(self):
        foreign, _ = self.start(engine=False, lock=self.config / "locks/dev.engine.lock")
        result = self.stop_selected()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("a dev-store engine lock is held", result.stderr)
        self.assertIsNone(foreign.poll(), result.stderr)

    def test_foreign_listener_is_preserved_and_reported(self):
        foreign, port = self.start(engine=False, listener=True)
        result = self.stop_selected(port)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("is held by something that is not a Painted Wolf Code engine", result.stderr)
        self.assertIsNone(foreign.poll(), result.stderr)

    def test_no_selection_leaves_an_installed_engine_running(self):
        installed, _ = self.start()
        self.restrict_name_lookup_to(installed)
        result = self.stop_selected()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIsNone(installed.poll(), result.stderr)

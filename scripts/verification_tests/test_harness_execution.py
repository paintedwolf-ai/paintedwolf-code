"""Preserved harness fixtures survive repeated process lifetimes."""

import json
import os
import shlex
import shutil
import socket
import subprocess
import sys
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock

from harness.seed_project import ensure_project
from harness.test_supervise import HarnessSignalTest, HarnessSupervisorTest


class HarnessProjectTest(unittest.TestCase):
    def test_preserves_existing_files_and_registration(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            fixture = root / "fixture"
            fixture.mkdir()
            (fixture / "source").write_text("original")
            for registered in (False, True):
                with self.subTest(registered=registered):
                    destination = root / f'project café "{registered}"'
                    destination.mkdir()
                    (destination / "source").write_text("user changes")
                    (destination / "new file").write_text("new work")
                    project = {"id": "retained", "roots": [{"path": str(destination)}]}
                    create = Mock(return_value=project)
                    result = ensure_project(fixture, destination, [project] if registered else [], create)
                    self.assertEqual(result["id"], "retained")
                    self.assertEqual((destination / "source").read_text(), "user changes")
                    self.assertEqual((destination / "new file").read_text(), "new work")
                    self.assertEqual(create.call_count, 0 if registered else 1)
                    if not registered:
                        self.assertEqual(json.loads(json.dumps(create.call_args.args[0]))["roots"][0]["path"], str(destination.resolve()))

    def test_fresh_fixture_is_seeded_once_and_renamed_project_is_reused(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            fixture = root / "fixture"
            fixture.mkdir()
            (fixture / "source").write_text("original")
            destination = root / "state" / "minimal-go-project"
            project = {"id": "fresh", "name": "Renamed", "roots": [{"path": str(destination)}]}
            create = Mock(return_value=project)
            ensure_project(fixture, destination, [], create)
            self.assertEqual((destination / "source").read_text(), "original")
            (destination / "source").unlink()
            ensure_project(fixture, destination, [project], create)
            self.assertFalse((destination / "source").exists())
            create.assert_called_once()

    def test_existing_non_directory_is_refused_without_replacement(self):
        with tempfile.TemporaryDirectory() as temp:
            destination = Path(temp) / "project"
            destination.write_text("keep")
            create = Mock()
            with self.assertRaises(ValueError):
                ensure_project(Path(temp), destination, [], create)
            create.assert_not_called()
            self.assertEqual(destination.read_text(), "keep")


class HarnessRuntimeTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="harness-runtime.")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.scripts = self.root / "scripts"
        source = Path(__file__).resolve().parents[1]
        for name in ("e2e-sidecar-bg.sh", "e2e-sidecar-serve.sh", "e2e-sidecar-stop.sh",
                     "e2e/sidecar-process.sh", "e2e/model-fixture.ts", "harness/crash-restart.sh"):
            target = self.scripts / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source / name, target)
        (self.scripts / "e2e/env.sh").write_text(":\n")
        (self.scripts / "e2e-sidecar-build.sh").write_text(
            'set -eu\nmkdir -p "$LYCAON_E2E_STATE_DIR/runtime"\n'
            'cp "$FIXTURE_BINARY" "$LYCAON_E2E_STATE_DIR/runtime/sidecar"\n'
            'echo build >> "$LYCAON_E2E_STATE_DIR/builds"\n')
        (self.scripts / "e2e-wait-health.sh").write_text(
            'set -eu\nfor _ in $(seq 1 100); do\n'
            '  [[ -s "$LYCAON_E2E_STATE_DIR/started-runtime" ]] && exit 0\n'
            '  sleep 0.02\ndone\nexit 1\n')
        self.binary = self.root / "fixture binary"
        self.write_binary("original")
        self.env = {k: v for k, v in os.environ.items()
                    if not k.startswith(("LYCAON_", "VITE_LYCAON_"))}
        self.env.update(LYCAON_HARNESS_MODE="mock", LYCAON_LLM_MOCK="1",
                        LYCAON_E2E_TOKEN="fixture", FIXTURE_BINARY=str(self.binary))
        self.states = []
        self.addCleanup(self.stop_states)

    def write_binary(self, value):
        self.binary.write_text(
            "#!/usr/bin/env bash\nexec " + shlex.quote(sys.executable) + " -c " + shlex.quote(
                "import os,time; from pathlib import Path; "
                "Path(os.environ['LYCAON_E2E_STATE_DIR'], 'started-runtime').write_text("
                + repr(value) + "); time.sleep(120)") + "\n")
        self.binary.chmod(0o700)

    def new_state(self):
        state = self.root / f"state café {len(self.states)}"
        state.mkdir()
        with socket.socket() as port:
            port.bind(("127.0.0.1", 0))
            addr = f"127.0.0.1:{port.getsockname()[1]}"
        env = dict(self.env, LYCAON_E2E_STATE_DIR=str(state), LYCAON_E2E_ADDR=addr,
                   LYCAON_E2E_API_URL=f"http://{addr}")
        self.states.append(env)
        return state, env

    def run_script(self, name, env, *args):
        return subprocess.run(["bash", str(self.scripts / name), *args], env=env,
                              capture_output=True, text=True, timeout=30)

    def stop_states(self):
        for env in self.states:
            self.run_script("e2e-sidecar-stop.sh", env)

    def start(self):
        state, env = self.new_state()
        result = self.run_script("e2e-sidecar-bg.sh", env)
        self.assertEqual(result.returncode, 0, result.stderr)
        return state, env

    def test_crash_reuses_prepared_binary_despite_broken_build(self):
        state, env = self.start()
        pid = (state / "sidecar.pid").read_text()
        self.write_binary("changed")
        (self.scripts / "e2e-sidecar-build.sh").write_text("exit 91\n")
        (state / "started-runtime").unlink()
        result = self.run_script("harness/crash-restart.sh", env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotEqual((state / "sidecar.pid").read_text(), pid)
        self.assertEqual((state / "started-runtime").read_text(), "original")
        self.assertEqual((state / "builds").read_text(), "build\n")

    def test_missing_runtime_does_not_kill_running_sidecar(self):
        state, env = self.start()
        pid = (state / "sidecar.pid").read_text()
        (state / "runtime/sidecar").unlink()
        result = self.run_script("harness/crash-restart.sh", env)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("leaving the running process untouched", result.stderr)
        self.assertEqual((state / "sidecar.pid").read_text(), pid)
        os.kill(int(pid), 0)

    def test_independent_states_keep_their_prepared_runtime(self):
        first, env = self.start()
        self.write_binary("second")
        second, _ = self.start()
        (first / "started-runtime").unlink()
        result = self.run_script("harness/crash-restart.sh", env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((first / "started-runtime").read_text(), "original")
        self.assertEqual((second / "started-runtime").read_text(), "second")

    def test_reuse_without_prepared_runtime_refuses_to_build(self):
        state, env = self.new_state()
        result = self.run_script("e2e-sidecar-bg.sh", env, "--reuse-built")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((state / "builds").exists())
        self.assertFalse((state / "sidecar.pid").exists())

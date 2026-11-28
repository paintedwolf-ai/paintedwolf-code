"""Both E2E launchers must offer a bindable private socket path even under deep artifact roots."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPTS = Path(__file__).resolve().parents[2]

# Stop during project fixture setup, before a build or a container starts.
COPY_PROBE = """#!/usr/bin/env python3
import json, os, socket
from pathlib import Path
state = Path(os.environ["LYCAON_E2E_STATE_DIR"])
directory = Path(os.environ["LYCAON_E2E_SOCKET_DIR"])
address = directory / "recovery-link.sock"
with socket.socket(socket.AF_UNIX) as server:
    server.bind(str(address))
    server.listen()
    print(json.dumps([str(state), str(directory), str(address.resolve())]))
raise SystemExit(73)
"""


class E2ESocketIsolationTests(unittest.TestCase):
    def test_web_and_desktop_can_bind_and_clean_up_after_setup_failure(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            deep = root / ("artifact-root-" * 10)
            deep.mkdir()
            tools = root / "bin"
            tools.mkdir()
            commands = {
                "docker": "#!/bin/sh\nexit 0\n",
                "bun": "#!/bin/sh\nexit 1\n",
                "cp": COPY_PROBE,
            }
            for name, script in commands.items():
                command = tools / name
                command.write_text(script)
                command.chmod(0o755)
            inherited = root / "caller-owned"
            inherited.mkdir()
            env = {
                "PATH": os.pathsep.join([str(tools), os.environ["PATH"]]),
                "HOME": temp,
                "TMPDIR": str(deep),
                "PW_ARTIFACT_ROOT": str(deep),
                "LYCAON_E2E_STATE_DIR": str(inherited),
                "LYCAON_E2E_SOCKET_DIR": str(inherited),
                "LYCAON_E2E_SHARDS": "1",
            }
            for launcher in ("e2e-den.sh", "e2e-den-desktop.sh"):
                with self.subTest(launcher=launcher):
                    result = subprocess.run(
                        ["bash", str(SCRIPTS / launcher)],
                        capture_output=True, text=True, env=env, timeout=30,
                    )
                    self.assertEqual(result.returncode, 73, result.stderr)
                    state, directory, address = map(Path, json.loads(result.stdout))
                    self.assertLessEqual(len(os.fsencode(address)), 103)
                    self.assertEqual(directory, address.parent)
                    self.assertTrue(inherited.is_dir())
                    self.assertFalse(state.exists(), state)
                    self.assertFalse(directory.exists(), directory)

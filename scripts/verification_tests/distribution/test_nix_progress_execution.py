"""Nix stderr activity remains distinct from its machine-readable result."""
import contextlib
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest

from scripts.linux_release import nix_runner as nix_progress


class NixProgressTests(unittest.TestCase):
    def test_failed_build_retains_exact_progress_and_stdout_json(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            progress = root / "progress.log"
            progress.write_bytes(b"earlier\n")
            console = io.StringIO()
            command = [sys.executable, "-c",
                       "import os;os.write(2,b'building fixture\\n');"
                       "os.write(1,b'{\"outputs\":[]}\\n');raise SystemExit(37)"]
            with contextlib.redirect_stderr(console):
                result = nix_progress.run(command, cwd=root,
                    env={**os.environ, "PW_TEST_STAGE_PROGRESS": str(progress)})
            self.assertEqual(result.returncode, 37)
            self.assertEqual(json.loads(result.stdout), {"outputs": []})
            self.assertEqual(progress.read_bytes(), b"earlier\nbuilding fixture\n")
            self.assertEqual(console.getvalue(), "building fixture\n")

    def test_unwritable_progress_destination_refuses_before_starting_nix(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            marker = root / "started"
            command = [sys.executable, "-c", "from pathlib import Path;Path('started').touch()"]
            with self.assertRaises(OSError):
                nix_progress.run(command, cwd=root,
                    env={**os.environ, "PW_TEST_STAGE_PROGRESS": str(root / "missing/progress")})
            self.assertFalse(marker.exists())

    def test_console_failure_drains_the_child_and_refuses_success(self):
        class BrokenConsole(io.StringIO):
            def write(self, value):
                raise OSError("console unavailable")

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            command = [sys.executable, "-c", "import os;os.write(2,b'x'*(1<<20));print('{}')"]
            with contextlib.redirect_stderr(BrokenConsole()), self.assertRaisesRegex(OSError, "console unavailable"):
                nix_progress.run(command, cwd=root,
                    env={**os.environ, "PW_TEST_STAGE_PROGRESS": str(root / "progress")})

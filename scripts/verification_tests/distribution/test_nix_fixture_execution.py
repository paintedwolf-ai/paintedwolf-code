"""The confined Nix suites receive only the locked fixture's exact store tools."""

import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release import nix_fixture

TOOLS = {"bash": "/nix/store/" + "a" * 32 + "-bash-5.3/bin/bash",
         "coreutils": "/nix/store/" + "b" * 32 + "-coreutils-9.8/bin/mkdir",
         "stdenv": "/nix/store/" + "c" * 32 + "-stdenv-linux"}


class NixFixtureTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.output = Path(self.temporary.name) / "fixture.json"
        self.output.write_text(json.dumps({"schema": 1, **TOOLS}))
        self.calls = []

    def run_nix(self, command, cwd, env, text, stdout):
        self.calls.append((command, env))
        return subprocess.CompletedProcess(command, 0, json.dumps([{"outputs": {"out": str(self.output)}}]))

    def build(self, machine="aarch64", run=None):
        with patch.object(nix_fixture.platform, "system", return_value="Linux"), \
                patch.object(nix_fixture.platform, "machine", return_value=machine), \
                patch.dict(nix_fixture.os.environ, {"GH_TOKEN": "private"}):
            return nix_fixture.build(Path("/checkout"), run=run or self.run_nix)

    def test_builds_this_checkouts_native_fixture_without_credentials(self):
        self.assertEqual(self.build(), {"PW_NIX_FIXTURE_BASH": TOOLS["bash"],
                                        "PW_NIX_FIXTURE_COREUTILS": TOOLS["coreutils"],
                                        "PW_NIX_FIXTURE_STDENV": TOOLS["stdenv"]})
        command, environment = self.calls[0]
        self.assertEqual(command[-1], "git+file:///checkout#packages.aarch64-linux.nix-workflow-fixture")
        self.assertIn("--no-write-lock-file", command)
        self.assertNotIn("GH_TOKEN", environment)

    def test_altered_fixtures_and_failed_builds_refuse(self):
        for altered in ({"schema": 2, **TOOLS}, {"schema": 1, **TOOLS, "extra": TOOLS["bash"]},
                        {"schema": 1, **TOOLS, "bash": "/usr/bin/bash"},
                        {"schema": 1, **TOOLS, "stdenv": TOOLS["stdenv"] + " extra"}):
            self.output.write_text(json.dumps(altered))
            with self.subTest(altered=altered), self.assertRaises(ValueError):
                self.build()
        with self.assertRaisesRegex(ValueError, "did not build"):
            self.build(run=lambda command, **kwargs: subprocess.CompletedProcess(command, 1, ""))
        with self.assertRaises(ValueError):
            self.build(machine="riscv64")


class NixFixtureCommandTests(unittest.TestCase):
    def test_command_prints_shell_exports_or_fails_without_output(self):
        import contextlib
        import io
        values = {"PW_NIX_FIXTURE_BASH": TOOLS["bash"], "PW_NIX_FIXTURE_STDENV": TOOLS["stdenv"] + "'x"}
        output = io.StringIO()
        with patch.object(nix_fixture, "build", return_value=values), contextlib.redirect_stdout(output):
            self.assertEqual(nix_fixture.main(), 0)
        self.assertEqual(output.getvalue().splitlines(),
                         [f"export PW_NIX_FIXTURE_BASH={TOOLS['bash']}",
                          f"export PW_NIX_FIXTURE_STDENV='{TOOLS['stdenv']}'\"'\"'x'"])
        output, errors = io.StringIO(), io.StringIO()
        with patch.object(nix_fixture, "build", side_effect=ValueError("did not build")), \
                contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
            self.assertEqual(nix_fixture.main(), 1)
        self.assertEqual(output.getvalue(), "")
        self.assertIn("did not build", errors.getvalue())


if __name__ == "__main__":
    unittest.main()

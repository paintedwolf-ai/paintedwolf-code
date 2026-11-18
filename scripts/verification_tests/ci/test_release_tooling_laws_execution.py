"""Release tooling keeps one source for what it ships and signs with."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

from verification_tests.ci.workflows import ROOT, commands, shell_array

IDENTITY = "Fixture Signing Identity"
IDENTIFIER = "test.fixture.identifier"


class ReleaseToolingLaws(unittest.TestCase):
    def test_notices_cover_exactly_the_go_binaries_the_engine_stages(self):
        staged = {argv[-1] for argv in commands((ROOT / "scripts/stage-engine.sh").read_text())
                  if argv[:2] == ["go", "build"] and argv[-1].startswith("./cmd/")}
        noticed = set(shell_array((ROOT / "scripts/licenses-notices.sh").read_text(), "SHIPPED_GO_PACKAGES"))
        self.assertTrue(staged)
        self.assertEqual(staged, noticed)

    def test_development_signing_and_provisioning_use_the_one_configured_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            scripts, tools = root / "scripts", root / "tools"
            scripts.mkdir()
            tools.mkdir()
            for name in ("sign-dev-binary.sh", "dev-signing-identity.sh"):
                shutil.copy2(ROOT / "scripts" / name, scripts)
            (scripts / "dev-signing-config.sh").write_text(
                f'readonly DEV_SIGNING_IDENTITY="{IDENTITY}"\nreadonly DEV_SIGNING_IDENTIFIER="{IDENTIFIER}"\n')
            calls = root / "calls"
            stubs = {"uname": "echo Darwin",
                     "security": f'echo "security $*" >> "{calls}"\n'
                                 f'[ "$1" = find-identity ] && echo \'  1) ABC "{IDENTITY}"\'; exit 0',
                     "codesign": f'echo "codesign $*" >> "{calls}"'}
            for name, body in stubs.items():
                (tools / name).write_text(f"#!/bin/sh\n{body}\n")
                (tools / name).chmod(0o755)
            target = root / "engine"
            target.write_text("binary")
            environment = {**os.environ, "PATH": f"{tools}{os.pathsep}{os.environ['PATH']}"}
            for command in (["sign-dev-binary.sh", str(target)], ["dev-signing-identity.sh", "--remove"]):
                result = subprocess.run(["bash", str(scripts / command[0]), *command[1:]],
                                        capture_output=True, text=True, env=environment)
                self.assertEqual(result.returncode, 0, result.stderr)
            recorded = calls.read_text()
        self.assertIn(f"--sign {IDENTITY} --identifier {IDENTIFIER}", recorded)
        self.assertIn(f"delete-identity -c {IDENTITY}", recorded)


if __name__ == "__main__":
    unittest.main()

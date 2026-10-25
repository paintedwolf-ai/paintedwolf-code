"""Nix generations ship user-namespace grants for exactly their own executables."""

import contextlib
import hashlib
import io
from pathlib import Path
import tempfile
import unittest

from scripts.linux_release import nix_apparmor


class NixAppArmorGrantTests(unittest.TestCase):
    def test_each_role_grants_only_its_literal_executable(self):
        executable = Path("/nix/store/" + "0" * 32 + "-tools/bin/opengrep-artifact")
        name, text = nix_apparmor.profile("scanner-builder", executable)
        self.assertEqual(name, "paintedwolf-scanner-builder-" + hashlib.sha256(str(executable).encode()).hexdigest())
        self.assertEqual(text, f'abi <abi/4.0>,\n\nprofile {name} "{executable}" flags=(unconfined) {{\n  userns,\n}}\n')
        self.assertTrue(nix_apparmor.profile("engine", executable)[0].startswith("paintedwolf-engine-"))
        self.assertEqual(nix_apparmor.literal('/nix/store/x-a"b*c[d]{e}^'), '"/nix/store/x-a\\"b\\*c\\[d\\]\\{e\\}\\^"')
        for role, path in (("browser", executable), ("engine", Path("bin/pw"))):
            with self.subTest(role=role), self.assertRaises(ValueError):
                nix_apparmor.profile(role, path)
        with self.assertRaises(ValueError):
            nix_apparmor.literal("/nix/store/x-engine\n")

    def test_command_writes_the_grant_inside_the_output_that_owns_the_executable(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory).resolve()
            executable = output / "bin/opengrep-artifact"
            executable.parent.mkdir()
            executable.write_text("")
            printed = io.StringIO()
            with contextlib.redirect_stdout(printed):
                self.assertEqual(nix_apparmor.main(["scanner-builder", str(executable), str(output)]), 0)
            written = Path(printed.getvalue().strip())
            self.assertEqual(written.parent, output / "share/painted-wolf-code/apparmor")
            self.assertIn(f'"{executable}"', written.read_text())
            other = Path(tempfile.mkdtemp(dir=directory))
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as refused:
                nix_apparmor.main(["engine", str(executable), str(other)])
            self.assertEqual(refused.exception.code, 1)
            self.assertFalse((other / "share").exists())


if __name__ == "__main__":
    unittest.main()

"""The assembled .deb declares exactly its /etc files as conffiles, so dpkg keeps local edits."""

import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[3]


def assembler():
    spec = importlib.util.spec_from_file_location("linux_package_assemble", ROOT / "scripts/linux-package-assemble.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def write(path, text="fixture"):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


class LinuxDebConffilesTests(unittest.TestCase):
    def assemble(self, files, links=()):
        """Build the .deb control area from a staged tree; dpkg-deb is a recorder on PATH."""
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        base = Path(directory.name)
        data = base / "staged/data"
        for relative in files:
            write(data / relative)
        for relative, target in links:
            (data / relative).parent.mkdir(parents=True, exist_ok=True)
            (data / relative).symlink_to(target)
        tools = base / "tools"
        tools.mkdir()
        calls = base / "dpkg-deb-calls"
        dpkg_deb = tools / "dpkg-deb"
        dpkg_deb.write_text('#!/bin/sh\nprintf "%s\\n" "$@" > "$DPKG_DEB_CALLS"\nfor last; do :; done\n: > "$last"\n',
                            encoding="utf-8")
        dpkg_deb.chmod(0o755)
        (base / "work").mkdir()
        (base / "out").mkdir()
        environment = {"PATH": f"{tools}{os.pathsep}{os.environ['PATH']}", "DPKG_DEB_CALLS": str(calls)}
        with mock.patch.dict(os.environ, environment):
            package = assembler().write_deb(ROOT, base / "staged", base / "work", "1.0.2~rc.1", "x86_64", base / "out")
        self.assertTrue(package.is_file())
        built = calls.read_text(encoding="utf-8").splitlines()
        self.assertEqual(built[-2:], [str(base / "work/deb-root"), str(package)])
        return base / "work/deb-root/DEBIAN"

    def test_conffiles_lists_exactly_the_regular_files_under_etc(self):
        control = self.assemble([
            "etc/apparmor.d/painted-wolf-code",
            "etc/painted-wolf-code/policy.d/site.conf",
            "opt/painted-wolf-code/bin/painted-wolf-code",
            "opt/painted-wolf-code/etc/defaults.conf",
            "usr/share/applications/dev.paintedwolf.code.desktop",
        ], links=[("etc/painted-wolf-code/current.conf", "policy.d/site.conf")])
        self.assertEqual((control / "conffiles").read_text(encoding="utf-8").splitlines(),
                         ["/etc/apparmor.d/painted-wolf-code", "/etc/painted-wolf-code/policy.d/site.conf"])
        summed = {line.split("  ", 1)[1] for line in (control / "md5sums").read_text(encoding="utf-8").splitlines()}
        self.assertEqual(summed, {"opt/painted-wolf-code/bin/painted-wolf-code", "opt/painted-wolf-code/etc/defaults.conf",
                                  "usr/share/applications/dev.paintedwolf.code.desktop"})
        self.assertEqual((control / "conffiles").stat().st_mode & 0o777, 0o644)

    def test_a_tree_without_etc_files_declares_no_conffiles(self):
        control = self.assemble(["opt/painted-wolf-code/bin/painted-wolf-code"])
        self.assertFalse((control / "conffiles").exists())
        self.assertTrue((control / "control").is_file())


if __name__ == "__main__":
    unittest.main()

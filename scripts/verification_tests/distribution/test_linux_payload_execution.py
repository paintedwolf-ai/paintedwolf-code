"""Native package payload completeness and maintainer effects are explicit."""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from linux_release.payload import load

ROOT = Path(__file__).resolve().parents[3]


def module(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), ROOT / "scripts" / f"{name}.py")
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


class LinuxPayloadTests(unittest.TestCase):
    def test_catalog_requires_valid_unique_helper_and_entry_point_roles(self):
        self.assertIn("pw-vault-identity", load()["helpers"])
        self.assertIn("pw-desktop-settings", load()["helpers"])
        for value in [{"format_version": 0}, {"format_version": 1, "helpers": ["pw", "pw"], "entry_points": ["pw"]},
                      {"format_version": 1, "helpers": ["pw"], "entry_points": ["missing"]}]:
            with tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / "payload.json"
                path.write_text(json.dumps(value))
                with self.assertRaises(ValueError):
                    load(path)

    def test_manifest_and_staging_refuse_missing_required_helpers(self):
        config = module("linux-package-config")
        manifest = module("linux-package-manifest")
        for missing in ("pw-vault-identity", "pw-desktop-settings"):
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                tauri = root / "lycaon-den/src-tauri"
                target = "x86_64-unknown-linux-gnu"
                shell = tauri / "target" / target / "release/painted-wolf-code"
                shell.parent.mkdir(parents=True)
                shell.write_text("fixture")
                shell.chmod(0o755)
                (tauri / "binaries").mkdir()
                for helper in load()["helpers"]:
                    if helper == missing:
                        continue
                    path = tauri / "binaries" / f"{helper}-{target}"
                    path.write_text("fixture")
                    path.chmod(0o755)
                with self.assertRaisesRegex(ValueError, missing):
                    config.required_files(root, tauri, target, root / "manifest.json")
                args = argparse.Namespace(root=root, target=target, version="1.0.0", output=root / "manifest.json")
                with self.assertRaisesRegex((ValueError, FileNotFoundError), missing):
                    manifest.generate(args)
    def test_optional_fragments_never_stage_a_selinux_module(self):
        config = module("linux-package-config")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for folder, name in [("selinux", "painted-wolf-code.pp"), ("systemd", "vault.service"), ("apparmor", "painted-wolf-code")]:
                path = root / "packaging/linux" / folder / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("fixture")
            deb, rpm = config.optional_files(root, "deb"), config.optional_files(root, "rpm")
            self.assertFalse(any("selinux" in path for path in {*deb, *rpm}))
            self.assertIn("/usr/lib/systemd/system/vault.service", rpm)
            self.assertIn("/etc/apparmor.d/painted-wolf-code", deb)
            self.assertNotIn("/etc/apparmor.d/painted-wolf-code", rpm)

    def test_rpm_removal_does_not_invoke_policy_tools_or_touch_user_state(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            commands = root / "commands"
            commands.mkdir()
            log = root / "policy-calls"
            tool = commands / "semodule"
            tool.write_text('#!/bin/sh\necho "$*" >> "$POLICY_CALLS"\necho painted-wolf-code\n')
            tool.chmod(0o755)
            for name in ("update-desktop-database", "gtk-update-icon-cache"):
                cache = commands / name
                cache.write_text("#!/bin/sh\nexit 0\n")
                cache.chmod(0o755)
            state = root / "home/vault"
            state.parent.mkdir()
            state.write_text("preserve")
            script = (ROOT / "packaging/linux/rpm/postun").read_text()
            script = script.replace("/opt/painted-wolf-code", str(root / "package"))
            script = script.replace("/run/systemd/system", str(root / "systemd"))
            for action in ("0", "1"):
                result = subprocess.run(["sh", "-s", "--", action], input=script,
                                        env={**os.environ, "PATH": str(commands) + os.pathsep + os.environ["PATH"],
                                             "POLICY_CALLS": str(log), "HOME": str(state.parent)},
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse(log.exists())
            self.assertEqual(state.read_text(), "preserve")

"""Native package payload completeness and maintainer effects are explicit."""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from verification_tests.distribution import test_protected_launch_packaging_execution as maintainer_fixture

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
        fixture = maintainer_fixture.MaintainerScripts()
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        log = fixture.root / "policy-calls"
        fixture.shim("semodule", f'echo "$*" >> "{log}"; echo painted-wolf-code')
        state = fixture.root / "home/vault"
        state.parent.mkdir()
        state.write_text("preserve")
        fixture.policy.mkdir(parents=True)
        policy = fixture.policy / "launch.json"
        policy.write_text("owned policy")
        foreign = fixture.root / "etc/foreign-policy"
        foreign.write_text("preserve foreign policy")
        identity = "a" * 64
        generation = fixture.install / "generations" / identity
        generation.mkdir(parents=True)
        (generation / "payload").write_text("owned generation")
        lock = fixture.locks / (identity + ".lock")
        for action in ("1", "0"):
            result = fixture.run_script("rpm", "postun", action)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(state.read_text(), "preserve")
            self.assertEqual(foreign.read_text(), "preserve foreign policy")
            self.assertFalse(log.exists())
            if action == "1":
                self.assertTrue(policy.exists())
                self.assertTrue(generation.exists())
                self.assertFalse(lock.exists())
            else:
                self.assertFalse(policy.exists())
                self.assertFalse(generation.exists())
                self.assertTrue(lock.is_file())
                self.assertEqual(lock.stat().st_mode & 0o777, 0o600)

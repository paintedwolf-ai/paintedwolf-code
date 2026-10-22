"""Native package preparation reuses exact archives without changing test dependencies."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[3]
SCRIPT = ROOT / ".github/actions/setup-linux-native/provision.py"
SPEC = importlib.util.spec_from_file_location("native_packages", SCRIPT)
NATIVE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(NATIVE)
DIGEST = "a" * 64
PLAN = f"'http://mirror/pool/a.deb' a_1_amd64.deb 42 SHA256:{DIGEST}\n"


class LinuxNativePackagesTests(unittest.TestCase):
    def test_identity_covers_dependencies_image_architecture_and_requested_packages(self):
        env = {"ImageOS": "ubuntu24", "ImageVersion": "20261022.1", "RUNNER_ARCH": "X64"}
        key = NATIVE.archive_identity(PLAN, ["xclip"], env)
        self.assertEqual(key, NATIVE.archive_identity(PLAN.replace("http://mirror", "http://other"), ["xclip"], env))
        for plan, names, environment in [
            (PLAN.replace(DIGEST, "b" * 64), ["xclip"], env),
            (PLAN.replace("a_1_", "a_2_"), ["xclip"], env),
            (PLAN, ["xclip", "sway"], env),
            (PLAN, ["xclip"], {**env, "ImageVersion": "20261023.1"}),
            (PLAN, ["xclip"], {**env, "RUNNER_ARCH": "ARM64"}),
        ]:
            self.assertNotEqual(key, NATIVE.archive_identity(plan, names, environment))
        for malformed in ["'url' a.deb 42 MD5Sum:abc", "'url' a.deb missing SHA256:abc", "'url' a.deb"]:
            with self.assertRaises(ValueError):
                NATIVE.archive_identity(malformed, ["xclip"], env)

    def test_cold_and_restored_archives_reach_the_same_package_manager_install(self):
        for restored in (False, True):
            with self.subTest(restored=restored), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / "packages.txt").write_text("xclip\nsway\n")
                calls = root / "calls.jsonl"
                sudo = root / "sudo"
                sudo.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
with open(os.environ['NATIVE_CALLS'], 'a') as output:
    output.write(json.dumps(sys.argv[1:]) + '\\n')
if '--print-uris' in sys.argv:
    print(os.environ['NATIVE_PLAN'])
elif 'install' in sys.argv:
    archive = next(a.split('=', 1)[1] for a in sys.argv if a.startswith('Dir::Cache::archives='))
    marker = pathlib.Path(archive) / 'a_1_amd64.deb'
    if os.environ['NATIVE_RESTORED'] == 'true':
        assert marker.read_bytes() == b'cached archive'
    marker.write_bytes(b'cached archive')
    sys.exit(int(os.environ.get('NATIVE_INSTALL_EXIT', '0')))
''')
                sudo.chmod(0o755)
                env = {**os.environ, "RUNNER_OS": "Linux", "RUNNER_TEMP": str(root),
                       "ImageOS": "ubuntu24", "ImageVersion": "20261022.1", "RUNNER_ARCH": "X64",
                       "GITHUB_OUTPUT": str(root / "output"), "NATIVE_CALLS": str(calls), "NATIVE_PLAN": PLAN,
                       "NATIVE_RESTORED": str(restored).lower(), "PATH": str(root) + os.pathsep + os.environ["PATH"]}
                with patch.object(NATIVE, "ROOT", root), patch.dict(os.environ, env):
                    NATIVE.main("plan")
                    output = (root / "output").read_text()
                    self.assertIn("key=linux-native-v1-", output)
                    archives = root / "paintedwolf-native-archives"
                    if restored:
                        (archives / "a_1_amd64.deb").write_bytes(b"cached archive")
                    NATIVE.main("install")
                    self.assertEqual((archives / "a_1_amd64.deb").read_bytes(), b"cached archive")
                    with patch.dict(os.environ, {"NATIVE_INSTALL_EXIT": "19"}), self.assertRaises(subprocess.CalledProcessError):
                        NATIVE.main("install")
                records = [json.loads(line) for line in calls.read_text().splitlines()]
                self.assertIn("update", records[0])
                self.assertIn("Acquire::ForceHash=sha256", records[1])
                self.assertIn("APT::Keep-Downloaded-Packages=true", records[2])
                self.assertEqual(records[2][-2:], ["xclip", "sway"])

    def test_invalid_package_names_and_non_linux_hosts_are_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for names in ("", "xclip\nxclip\n", "--allow-unauthenticated\n", "xclip;true\n"):
                (root / "packages.txt").write_text(names)
                with patch.object(NATIVE, "ROOT", root), self.assertRaises(ValueError):
                    NATIVE.packages()
            with patch.dict(os.environ, {"RUNNER_OS": "macOS"}), self.assertRaises(ValueError):
                NATIVE.main("plan")

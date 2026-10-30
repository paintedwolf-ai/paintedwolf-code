"""Cache identity probes work before the runner has a Go executable."""
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import yaml

from ci_policy import cache_paths


class GoCachePathTests(unittest.TestCase):
    def test_export_pins_later_builds_to_the_lookup_payload(self):
        with tempfile.TemporaryDirectory() as directory:
            scratch = Path(directory)
            output, exported, cache = scratch / "output", scratch / "env", scratch / "compiler"
            environment = dict(RUNNER_OS="Linux", GOCACHE=str(cache),
                               GITHUB_OUTPUT=str(output), GITHUB_ENV=str(exported))
            with patch.dict(os.environ, environment, clear=True):
                cache_paths.main()
            self.assertEqual(output.read_text(), f"gocache={cache}\n")
            self.assertEqual(exported.read_text(), f"GOCACHE={cache}\n")
            self.assertFalse(cache.exists(), "export must not provision a payload")

    def test_real_action_resolves_macos_cache_without_go(self):
        root = Path(__file__).resolve().parents[3]
        action = yaml.safe_load((root / ".github/actions/cache-go/action.yml").read_text())
        step = next(step for step in action["runs"]["steps"] if step.get("id") == "paths")
        bash = shutil.which("bash")
        self.assertIsNotNone(bash)
        with tempfile.TemporaryDirectory() as directory:
            scratch = Path(directory)
            binaries = scratch / "bin"
            binaries.mkdir()
            python = binaries / "python3"
            python.write_text(f'#!{bash}\nexec {shlex.quote(sys.executable)} "$@"\n')
            python.chmod(0o755)
            output, exported = scratch / "output", scratch / "env"
            environment = dict(PATH=str(binaries), HOME=str(scratch), RUNNER_OS="macOS",
                               RUNNER_ARCH="ARM64", ImageOS="macos15",
                               GITHUB_OUTPUT=str(output), GITHUB_ENV=str(exported))
            result = subprocess.run([bash, "--noprofile", "--norc", "-c", step["run"]],
                                    cwd=root, env=environment, text=True, capture_output=True,
                                    check=False)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            cache = scratch / "Library/Caches/go-build"
            lines = output.read_text().splitlines()
            self.assertIn(f"gocache={cache}", lines)
            self.assertTrue(any(line.startswith("key_prefix=go-v4-macOS-ARM64-macos15-go")
                                for line in lines))
            self.assertEqual(exported.read_text(), f"GOCACHE={cache}\n")
            self.assertFalse(cache.exists(), "identity lookup must not provision a payload")

    def test_native_defaults_and_explicit_cache_override(self):
        cases = [
            ({"RUNNER_OS": "macOS", "HOME": "/home/user", "XDG_CACHE_HOME": "/ignored"},
             "/home/user/Library/Caches/go-build"),
            ({"RUNNER_OS": "Linux", "HOME": "/home/user"}, "/home/user/.cache/go-build"),
            ({"RUNNER_OS": "Linux", "XDG_CACHE_HOME": "/cache"}, "/cache/go-build"),
            ({"RUNNER_OS": "Windows", "LocalAppData": "C:\\Users\\user\\AppData\\Local"},
             "C:\\Users\\user\\AppData\\Local\\go-build"),
            ({"RUNNER_OS": "Windows", "GOCACHE": "\\\\server\\cache\\go"}, "\\\\server\\cache\\go"),
            ({"RUNNER_OS": "Linux", "GOCACHE": "/explicit"}, "/explicit"),
        ]
        for environment, expected in cases:
            with self.subTest(environment=environment):
                self.assertEqual(cache_paths.go_cache_directory(environment), expected)

    def test_invalid_cache_context_is_refused(self):
        cases = [
            {"RUNNER_OS": "Linux", "GOCACHE": "off"},
            {"RUNNER_OS": "Linux", "GOCACHE": "relative"},
            {"RUNNER_OS": "Linux", "HOME": "/home/user", "XDG_CACHE_HOME": "relative"},
            {"RUNNER_OS": "Windows", "LOCALAPPDATA": "C:relative"},
            {"RUNNER_OS": "macOS"},
            {"RUNNER_OS": "Windows"},
            {"RUNNER_OS": "unknown", "GOCACHE": "/cache"},
            {"RUNNER_OS": "Linux", "GOCACHE": "/cache\ninvalid"},
            {"RUNNER_OS": "Linux", "HOME": "/cache\rinvalid"},
            {"RUNNER_OS": "Linux", "GOCACHE": "/cache\0invalid"},
        ]
        for environment in cases:
            with self.subTest(environment=environment), self.assertRaises(ValueError):
                cache_paths.go_cache_directory(environment)

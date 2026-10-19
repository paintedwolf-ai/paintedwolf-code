"""Managed Nix operations keep source/lock identity and publication credentials separate."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release import nix


class NixExecutionTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.calls = self.root / "calls.jsonl"
        binary = self.root / "bin"
        binary.mkdir()
        tool = binary / "nix"
        tool.write_text("#!" + sys.executable + "\n" + '''import json,os,pathlib,sys
with pathlib.Path(os.environ['NIX_FIXTURE_CALLS']).open('a') as output:
 output.write(json.dumps({'args':sys.argv[1:], 'credentials':any(name in os.environ for name in ['GH_TOKEN','LINUX_PACKAGE_SIGNING_KEY','FEED_SIGNING_KEYS_JSON','TAURI_SIGNING_PRIVATE_KEY'])})+'\\n')
if 'build' in sys.argv: print(json.dumps([{'drvPath':'/nix/store/fixture.drv','outputs':{'out':'/nix/store/fixture'}}]))
if 'path-info' in sys.argv: print(json.dumps({'/nix/store/fixture':{'references':[],'narHash':'sha256-fixture'}}))
sys.exit(int(os.environ.get('NIX_FIXTURE_EXIT','0')))
''')
        tool.chmod(0o755)
        (self.root / "flake.lock").write_text('{"version":7}')
        subprocess.run(["git", "init", "-q", str(self.root)], check=True)
        subprocess.run(["git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                        "commit", "-q", "--allow-empty", "-m", "fixture"], cwd=self.root, check=True)
        self.environment = {"PATH": str(binary) + os.pathsep + os.environ["PATH"],
                            "NIX_FIXTURE_CALLS": str(self.calls),
                            "PW_TEST_ARTIFACT_ROOT": str(self.root / "artifacts"),
                            "PW_ARTIFACT_ROOT": str(self.root / "artifacts"),
                            "GH_TOKEN": "private", "LINUX_PACKAGE_SIGNING_KEY": "private",
                            "FEED_SIGNING_KEYS_JSON": "private", "TAURI_SIGNING_PRIVATE_KEY": "private"}

    def invoke(self, operation):
        with patch.dict(os.environ, self.environment), patch.object(nix.platform, "system", return_value="Linux"), \
                patch.object(nix.platform, "machine", return_value="x86_64"):
            return nix.execute(operation, self.root)

    def reports(self):
        return [json.loads(path.read_text()) for path in (self.root / "artifacts/nix").glob("*.json")]

    def test_evaluation_keeps_the_lock_and_cannot_receive_publication_credentials(self):
        before = (self.root / "flake.lock").read_bytes()
        self.assertEqual(self.invoke("check"), 0)
        self.assertEqual((self.root / "flake.lock").read_bytes(), before)
        call = json.loads(self.calls.read_text())
        self.assertIn("--no-build", call["args"])
        self.assertIn("--no-write-lock-file", call["args"])
        self.assertFalse(call["credentials"])
        report = self.reports()[0]
        self.assertTrue(report["passed"])
        self.assertEqual(len(report["source_tree"]), 40)

    def test_build_retains_closure_and_refuses_unsupported_hosts(self):
        self.assertEqual(self.invoke("build"), 0)
        calls = [json.loads(line) for line in self.calls.read_text().splitlines()]
        self.assertIn("--no-link", calls[0]["args"])
        self.assertIn("--recursive", calls[1]["args"])
        self.assertTrue(all(not call["credentials"] for call in calls))
        self.assertEqual(self.reports()[0]["builds"][0]["outputs"]["out"], "/nix/store/fixture")
        self.assertIn("/nix/store/fixture", self.reports()[0]["closure"])
        with patch.object(nix.platform, "system", return_value="Darwin"), self.assertRaises(ValueError):
            nix.command("build", self.root)

    def test_failures_have_a_failed_report_and_missing_lock_never_runs_nix(self):
        self.environment["NIX_FIXTURE_EXIT"] = "29"
        self.assertEqual(self.invoke("check"), 29)
        self.assertFalse(self.reports()[0]["passed"])
        self.calls.unlink()
        (self.root / "flake.lock").unlink()
        with self.assertRaises(ValueError):
            self.invoke("check")
        self.assertFalse(self.calls.exists())

    def test_module_fixture_build_uses_checks_and_keeps_native_architecture(self):
        for machine, system in [("x86_64", "x86_64-linux"), ("aarch64", "aarch64-linux")]:
            with self.subTest(machine=machine), patch.object(nix.platform, "system", return_value="Linux"), \
                    patch.object(nix.platform, "machine", return_value=machine):
                command = nix.command("build", self.root, "nixos-module")
                self.assertEqual(command[-1], "git+file://" + str(self.root) + "#checks." + system + ".nixos-module")
                self.assertIn("--no-write-lock-file", command)

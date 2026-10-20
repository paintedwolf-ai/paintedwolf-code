"""Installed observations remain bounded and incomplete interactions fail closed."""
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

from scripts.linux_release.installed_cases.app_api import EngineAPI
from scripts.linux_release.installed_campaign import command, verify_execution_receipt
from scripts.linux_release.installed_cases.desktop import execute
from scripts.linux_release.installed_cases.records import retain
from scripts.linux_release.installed_execution import driver_family, execute_case
from scripts.linux_release.installed_transfer import export, receive


class InstalledHelperTests(unittest.TestCase):
    def test_observation_retention_is_exclusive_and_does_not_promote_missing_cases(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binding = {"source_commit": "a" * 40, "artifact": {"installation": "deb"}}
            result = execute(binding, "windowing", "resize", root)
            self.assertEqual(result["status"], "unverified")
            self.assertTrue(result["evidence"])
            with self.assertRaises(FileExistsError):
                retain(binding, "windowing", "resize", root, "passed", {})
            with self.assertRaises(ValueError):
                retain(binding, "../escape", "case", root, "passed", {})

    def test_registry_selects_nix_only_for_nix_installations(self):
        self.assertEqual(driver_family("updates", "deb"), "packages")
        self.assertEqual(driver_family("updates", "nix"), "nix")
        self.assertEqual(driver_family("session", "rpm"), "security")

    def test_api_rejects_external_and_redirect_authority_before_network(self):
        api = EngineAPI(19876, "private-token")
        for path in ("https://example.com/v1/sessions", "//example.com", "/other", "/v1/sessions#fragment"):
            with self.assertRaises(ValueError):
                api.request("GET", path)
        with self.assertRaises(ValueError):
            api.request("TRACE", "/health")

    def test_transfer_round_trip_and_refuses_traversal_and_symlinks(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            leaf = root / "leaf"
            leaf.mkdir()
            for name in ("campaign.json", "binding.json", "driver-registry.json", "execution.json"):
                (leaf / name).write_text("{}")
            archive = root / "leaf.zip"
            with archive.open("wb") as stream:
                export(leaf, stream)
            receive(archive, root / "received")
            self.assertEqual((root / "received/campaign.json").read_text(), "{}")
            (leaf / "link").symlink_to(leaf / "campaign.json")
            with self.assertRaises(ValueError):
                export(leaf, io.BytesIO())
            with zipfile.ZipFile(archive, "w") as stream:
                stream.writestr("../escape", "unsafe")
            with self.assertRaises(ValueError):
                receive(archive, root / "unsafe")

    def test_guest_command_pins_ssh_identity_and_quotes_paths(self):
        target = {"id": "noble", "host": "127.0.0.1", "port": 45557, "user": "tester",
                  "identity_file": "/private/key", "known_hosts": "/private/known_hosts",
                  "checkout": "/checkout with spaces", "runtime_bin_dirs": ["/toolchain/bin"],
                  "artifact_id": "deb-amd64", "environment_id": "ubuntu-24-gnome",
                  "final_artifacts": "/control/final.json", "file_root": "/packages",
                  "install_root": "/opt/painted-wolf-code", "output": "/fresh/output",
                  "nix_program": None, "case_inputs": None}
        result = command(target, ["./task", "test:linux:installed", "--", "--run-cases"])
        self.assertIn("StrictHostKeyChecking=yes", result)
        self.assertIn("IdentitiesOnly=yes", result)
        self.assertIn("cd '/checkout with spaces'", result[-1])
        self.assertIn("exec env PATH=/toolchain/bin:/usr/local/bin:/usr/bin:/bin ./task", result[-1])
        for field, value in (("host", "-oProxyCommand=untrusted"), ("port", True),
                             ("runtime_bin_dirs", ["/bin:untrusted"])):
            invalid = {**target, field: value}
            with self.assertRaises(ValueError):
                command(invalid, ["./task"])
        with self.assertRaises(ValueError):
            command({**target, "command": "untrusted"}, ["./task"])

    def test_stale_leaf_output_cannot_replace_a_fresh_execution(self):
        target = {"artifact_id": "selected", "environment_id": "guest"}
        request = {"format_version": 1, "execution_nonce": "a" * 32, "source_commit": "b" * 40,
                   "driver_tree": "c" * 40, **target, "variant_id": None}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "leaf").mkdir()
            (root / "execution-request.json").write_text(json.dumps(request))
            (root / "leaf/execution.json").write_text(json.dumps(request))
            self.assertEqual(verify_execution_receipt(root, target, "b" * 40, "c" * 40), request)
            for name, value in (("execution_nonce", "d" * 32), ("driver_tree", "e" * 40), ("format_version", True)):
                (root / "leaf/execution.json").write_text(json.dumps({**request, name: value}))
                with self.assertRaises(ValueError):
                    verify_execution_receipt(root, target, "b" * 40, "c" * 40)

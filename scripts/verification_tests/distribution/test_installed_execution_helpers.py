"""Installed observations remain bounded and incomplete interactions fail closed."""
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

from scripts.linux_release.installed_cases.app_api import EngineAPI
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
            for name in ("campaign.json", "binding.json", "driver-registry.json"):
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

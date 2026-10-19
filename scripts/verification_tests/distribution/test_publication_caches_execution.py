"""Cache admission uses protected trust and actual verification before authorization."""

import json
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import Mock, patch

from linux_release.publication_caches import verify


class PublicationCachesTests(unittest.TestCase):
    def fixture(self, root):
        request = {"system": "x86_64-linux", "mode": "production", "outputs": ["output"]}
        policy = {"schema_version": 1, "mode": "production", "public_keys": ["protected-key"]}
        report = {"request": request, "public_keys": policy["public_keys"], "closure_sha256": "b" * 64}
        directory = root / "nix-cache/x86_64-linux"
        directory.mkdir(parents=True)
        (directory / "report.json").write_text(json.dumps(report))
        (root / "policy.json").write_text(json.dumps(policy))
        selection = {"source_commit": "a" * 40, "artifacts": {"format_version": 1,
            "source_commit": "a" * 40, "artifacts": [{"id": "nix", "installation": "nix-profile",
            "architecture": "amd64", "source_commit": "a" * 40,
            "identity": {"kind": "nix-closure", "sha256": "c" * 64,
                         "store_path": "/nix/store/" + "a" * 32 + "-app", "lock_sha256": "d" * 64}}]}}
        modules = {name: types.ModuleType(name) for name in ["admission", "publication", "release_authority"]}
        modules["admission"].validate_request = Mock()
        modules["publication"].verified_report = Mock(return_value=report)
        modules["release_authority"].authorize = Mock(return_value=True)
        return selection, policy, report, modules

    def test_selected_protected_trust_precedes_actual_nar_verification_and_release_authority(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            selection, policy, report, modules = self.fixture(root)
            with patch("linux_release.publication_caches.POLICY", root / "policy.json"), \
                 patch.dict(sys.modules, {"scripts.nix_cache." + name: module for name, module in modules.items()}):
                result = verify(root, selection)
            self.assertEqual(result[0]["system"], "x86_64-linux")
            modules["admission"].validate_request.assert_called_once_with(report["request"], policy)
            modules["publication"].verified_report.assert_called_once_with(root / "nix-cache/x86_64-linux/cache", report)
            modules["release_authority"].authorize.assert_called_once_with(selection, report["request"], report, trust_policy=policy)

    def test_report_cannot_choose_its_own_trust_or_architecture(self):
        for mutation in ["key", "system", "mode"]:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                selection, _, report, modules = self.fixture(root)
                if mutation == "key":
                    report["public_keys"] = ["attacker-key"]
                else:
                    report["request"][mutation] = "aarch64-linux" if mutation == "system" else "rehearsal"
                (root / "nix-cache/x86_64-linux/report.json").write_text(json.dumps(report))
                with patch("linux_release.publication_caches.POLICY", root / "policy.json"), \
                     patch.dict(sys.modules, {"scripts.nix_cache." + name: module for name, module in modules.items()}):
                    with self.assertRaises(ValueError):
                        verify(root, selection)
                modules["publication"].verified_report.assert_not_called()
                modules["release_authority"].authorize.assert_not_called()

    def test_authority_refusal_after_actual_cache_verification_is_not_accepted(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            selection, _, _, modules = self.fixture(root)
            modules["release_authority"].authorize.return_value = False
            with patch("linux_release.publication_caches.POLICY", root / "policy.json"), \
                 patch.dict(sys.modules, {"scripts.nix_cache." + name: module for name, module in modules.items()}):
                with self.assertRaisesRegex(ValueError, "independently admitted"):
                    verify(root, selection)

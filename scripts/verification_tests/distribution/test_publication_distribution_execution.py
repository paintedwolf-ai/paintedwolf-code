"""The Linux campaign never grants unrelated distribution publication authority."""
import copy
from pathlib import Path
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release.publication_distribution import DistributionAuthority

OWNER = "scripts.linux_release.publication_distribution"


class DistributionAuthorityTests(unittest.TestCase):
    def setUp(self):
        self.selected = Mock(root=Path("/retained"), source={"source_commit": "a" * 40,
                            "source_tree": "b" * 40, "flake_lock_sha256": "c" * 64})
        self.inputs = {"version": "1.2.3", "channel": "stable", "artifacts": {}}
        self.selected.authorize.return_value = self.inputs, {"accepted": True}
        self.binding = {"document": {"source": {"commit": "a" * 40, "tree": "b" * 40},
                        "version": "1.2.3", "channel": "stable", "nix": None,
                        "platforms": {"linux-x86_64": {"packages": {"deb": {"sha256": "d" * 64, "bytes": 3}}}}},
                        "catalog": Path("catalog"), "updater_manifest": Path("updater"),
                        "artifacts": Path("files"), "request": {}}
        self.rows = {"native.deb": {"architecture": "amd64", "installation": "deb",
                                     "identity": {"kind": "file"}}}
        self.files = [{"id": "native.deb", "sha256": "d" * 64, "bytes": 3}]

    def invoke(self, callback):
        with patch(OWNER + ".artifacts", return_value=self.rows), patch(OWNER + ".verify_files", return_value=self.files):
            return DistributionAuthority(self.selected, authorize_release=callback)(self.binding)

    def test_every_callback_reauthorizes_and_requires_independent_owner(self):
        callback = Mock(return_value=True)
        self.assertTrue(self.invoke(callback))
        self.assertTrue(self.invoke(callback))
        self.assertEqual(self.selected.authorize.call_count, 2)
        self.assertEqual(callback.call_count, 2)
        callback.assert_called_with(self.binding, self.inputs, {"accepted": True})

    def test_truthy_or_refused_independent_permission_is_not_authority(self):
        for result in (False, 1, "accepted", None):
            with self.subTest(result=result), self.assertRaisesRegex(ValueError, "independent release"):
                self.invoke(Mock(return_value=result))
        with self.assertRaisesRegex(ValueError, "independent catalog"):
            DistributionAuthority(self.selected, authorize_release=None)

    def test_changed_source_and_native_bytes_refuse_before_other_owner(self):
        for field, value in (("sha256", "e" * 64), ("bytes", 4)):
            binding = copy.deepcopy(self.binding)
            self.binding["document"]["platforms"]["linux-x86_64"]["packages"]["deb"][field] = value
            callback = Mock(return_value=True)
            with self.assertRaisesRegex(ValueError, "installed bytes"):
                self.invoke(callback)
            callback.assert_not_called()
            self.binding = binding
        self.binding["document"]["source"]["tree"] = "f" * 40
        with self.assertRaisesRegex(ValueError, "Linux source"):
            self.invoke(Mock(return_value=True))

    def test_nix_lock_and_incomplete_binding_refuse(self):
        self.binding["document"]["nix"] = {"source": {"commit": "a" * 40, "flake_lock_sha256": "f" * 64}}
        with self.assertRaisesRegex(ValueError, "admitted lock"):
            self.invoke(Mock(return_value=True))
        del self.binding["catalog"]
        with self.assertRaisesRegex(ValueError, "complete constructor"):
            self.invoke(Mock(return_value=True))


class InactiveDeliveryAuthorityTests(unittest.TestCase):
    def fixture(self):
        from scripts.linux_release.publication_distribution import InactiveDeliveryReleaseAuthority
        selected = Mock()
        selected.authorize.return_value = {"provenance": {}, "metadata": {}, "non_linux_packages": {}}
        binding = {"document": {"mode": "production", "origin": "https://downloads.paintedwolf.dev",
                    "controller_commit": "a" * 40, "repositories": [], "aur": {"enabled": False},
                    "nix": {"cache": {"enabled": False}}}}
        return selected, binding, InactiveDeliveryReleaseAuthority(selected, "a" * 40)

    def test_every_callback_requires_fresh_scoped_release_facts(self):
        selected, binding, authority = self.fixture()
        self.assertTrue(authority(binding, {}, {"accepted": True}))
        self.assertTrue(authority(binding, {}, {"accepted": True}))
        self.assertEqual(selected.authorize.call_count, 2)
        selected.authorize.return_value = True
        with self.assertRaisesRegex(ValueError, "facts"):
            authority(binding, {}, {"accepted": True})

    def test_local_control_success_cannot_announce_any_active_delivery(self):
        mutations = [("repositories", [{"signed_generation": "local"}]), ("aur", {"enabled": True}),
                     ("nix", {"cache": {"enabled": True}}), ("controller_commit", "b" * 40)]
        for field, value in mutations:
            with self.subTest(field=field):
                _, binding, authority = self.fixture()
                binding["document"][field] = value
                with self.assertRaisesRegex(ValueError, "availability"):
                    authority(binding, {}, {"accepted": True})

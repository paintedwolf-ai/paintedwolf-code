"""Advertised availability requires fresh transport witnesses on each callback."""
import copy
from pathlib import Path
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release.publication.delivery.delivery import ObservedDeliveryReleaseAuthority

OWNER = "scripts.linux_release.publication.delivery.delivery"


class DeliveryAuthorityTests(unittest.TestCase):
    def fixture(self):
        selected, released, backend = Mock(), Mock(), Mock()
        selected.authorize.return_value = ({"source_commit": "a" * 40}, {"accepted": True})
        released.authorize.return_value = {"provenance": {}, "metadata": {}, "non_linux_packages": {}}
        authority = ObservedDeliveryReleaseAuthority(selected, released, "b" * 40, repositories=backend)
        binding = {"document": {"mode": "production", "origin": "https://downloads.paintedwolf.dev",
                   "controller_commit": "b" * 40, "aur": {"enabled": False}, "repositories": [{"generation": "c"}],
                   "nix": {"cache": {"enabled": False}}}}
        return selected, released, authority, binding

    def test_signing_callbacks_repeat_both_owners_and_current_delivery(self):
        selected, released, authority, binding = self.fixture()
        with patch(OWNER + ".repository_facts", return_value=[{"generation": "c"}]) as repository, \
             patch(OWNER + ".cache_facts", return_value={"enabled": False}) as cache:
            self.assertTrue(authority(binding, {}, {"accepted": True}))
            self.assertTrue(authority(binding, {}, {"accepted": True}))
            self.assertEqual(repository.call_count, 2)
            self.assertEqual(cache.call_count, 2)
            self.assertEqual(selected.authorize.call_count, 4)
            self.assertEqual(released.authorize.call_count, 2)
            repository.side_effect = ValueError("withdrawn during public observation")
            with self.assertRaisesRegex(ValueError, "withdrawn"):
                authority(binding, {}, {"accepted": True})

    def test_changed_advertised_generation_cache_and_aur_refuse(self):
        for mutation in ({"repositories": [{"generation": "stale"}]},
                         {"nix": {"cache": {"enabled": True}}}, {"aur": {"enabled": True}}):
            with self.subTest(mutation=mutation):
                _, _, authority, binding = self.fixture()
                binding["document"].update(mutation)
                with patch(OWNER + ".repository_facts", return_value=[{"generation": "c"}]), \
                     patch(OWNER + ".cache_facts", return_value={"enabled": False}), self.assertRaises(ValueError):
                    authority(binding, {}, {"accepted": True})

    def test_saved_truthy_release_receipt_does_not_authorize_delivery(self):
        _, released, authority, binding = self.fixture()
        released.authorize.return_value = True
        with self.assertRaisesRegex(ValueError, "independent release"):
            authority(binding, {}, {"accepted": True})
        released.authorize.return_value = {"provenance": {}, "metadata": {}, "non_linux_packages": {}}
        with self.assertRaisesRegex(ValueError, "independent release"):
            authority(binding, {}, {"accepted": False})


class RepositoryDeliveryFactsTests(unittest.TestCase):
    def test_pacman_config_is_derived_without_mutating_retained_generation(self):
        import tempfile
        from scripts.linux_release.publication.delivery.delivery import repository_facts
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            public = root / "trusted.asc"; public.write_bytes(b"selected public key")
            manifest = {"family": "pacman", "repository": "painted-wolf-code", "key_fingerprint": "A" * 40}
            facts = {"family": "pacman", "architecture": "x86_64", "channel": "stable", "generation": "a" * 64,
                     "inventory_sha256": "b" * 64, "activation_sha256": "c" * 64,
                     "base_url": "https://downloads.paintedwolf.dev/linux/repositories/channels/pacman/stable/x86_64",
                     "signing_generation": 1, "key_fingerprint": "A" * 40}
            backend = Mock(public="https://downloads.paintedwolf.dev/linux/repositories")
            backend.verify_public.side_effect = lambda name, path, **kw: self.assertTrue(path.is_file())
            selected = Mock(root=root)
            with patch(OWNER + ".selection", return_value=({}, public)), patch(OWNER + ".digest", return_value="a" * 64), patch(OWNER + ".observe", return_value={"facts": facts}):
                result = repository_facts(selected, {"repositories": {"generations": [{"manifest": manifest, "operation": {}}]}}, {"verified_setups": []}, backend)
            self.assertEqual(result[0]["setup"]["file"], "client.conf")
            self.assertIn("/generations/" + "a" * 64 + "/client.conf", result[0]["setup"]["url"])
            self.assertFalse((root / "generations").exists())
            self.assertEqual(backend.verify_public.call_count, 2)

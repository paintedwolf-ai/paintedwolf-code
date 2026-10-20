"""Delivery staging preserves complete admission and immutable setup identities."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch
from scripts.linux_release.publication import delivery_publication as owner
from scripts.linux_release.publication.immutable import identity, publish_setups

class DeliveryTransportTests(unittest.TestCase):
    def test_complete_stage_precedes_setups_cache_and_any_activation(self):
        events = []
        selected = Mock(root=Path("/selected"))
        entries = [{"manifest": {"family": family}} for family in ("deb", "pacman")]
        inputs = {"repositories": {"generations": entries}}
        selected.authorize.return_value = inputs, {"accepted": True}
        def transport(manifest, directory, catalog, prefix, origin, expected, rehearsal, operation, phase, **kwargs):
            events.append((phase, manifest["family"]))
            return {"phase": phase}
        with patch.object(owner, "RepositoryAuthority"), patch.object(owner, "digest", side_effect=lambda row: row["family"]), patch("scripts.linux_release.repository_r2.publish_r2", side_effect=transport), patch("scripts.linux_release.pacman_r2.publish_r2", side_effect=transport), patch.object(owner, "publish_setups", side_effect=lambda *a, **k: events.append(("setups", None))), patch.object(owner, "publish_caches", side_effect=lambda *a: events.append(("cache", None))):
            owner.publish(selected, origin="https://downloads.paintedwolf.dev", expected={"deb": None, "pacman": None}, immutable_backend=Mock())
            self.assertEqual(events, [("stage", "deb"), ("stage", "pacman"), ("setups", None), ("cache", None), ("activate", "deb"), ("activate", "pacman")])
            events.clear()
            with self.assertRaisesRegex(ValueError, "every"):
                owner.publish(selected, origin="https://downloads.paintedwolf.dev", expected={"deb": None})
            self.assertEqual(events, [])

    def test_setup_changed_during_publication_refuses_continuation(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            directory = root / "repository-setup" / "a"
            directory.mkdir(parents=True)
            for name in ("setup.deb", "SHA256SUMS", "SHA256SUMS.asc", "package-key.asc"):
                (directory / name).write_bytes(b"selected " + name.encode())
            setup = {"repository_generation": "a", "family": "deb", "package": {"file": "setup.deb", **identity(directory / "setup.deb")}}
            selected = Mock(root=root)
            selected.authorize.return_value = ({"repositories": {"generations": [{"manifest": {}}]}}, {"verified_setups": [setup]})
            backend = Mock()
            backend.put.side_effect = lambda *a: (directory / "setup.deb").write_bytes(b"changed")
            with patch("scripts.linux_release.publication.immutable.digest", return_value="a"), self.assertRaisesRegex(ValueError, "changed"):
                publish_setups(selected, backend=backend)
            self.assertEqual(backend.put.call_count, 1)

    def test_delivery_predecessor_policy_is_owner_controlled_and_exact(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "policy.json"
            path.write_text(json.dumps({"format_version": 1, "expected": {"a": None}}))
            path.chmod(0o600)
            self.assertEqual(owner.load_policy(path), {"a": None})
            path.chmod(0o622)
            with self.assertRaisesRegex(ValueError, "owner-controlled"):
                owner.load_policy(path)
            path.chmod(0o600)
            path.write_text(json.dumps({"format_version": True, "expected": {}}))
            with self.assertRaisesRegex(ValueError, "predecessors"):
                owner.load_policy(path)

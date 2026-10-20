"""Immutable metadata and notifications recheck live authority rather than saved receipts."""
from contextlib import ExitStack
import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release.publication import document_publication as owner
from scripts.release_distribution_contract.document_schema import canonical


class DocumentTransportTests(unittest.TestCase):
    def fixture(self, root):
        document = {"version": "1.2.3", "channel": "stable", "signing_generation": 1,
                    "updater_manifest_sha256": "a" * 64}
        (root / "distribution.v1.json").write_bytes(canonical(document))
        (root / "distribution.v1.json.sig").write_text("signed fixture")
        stack = ExitStack()
        self.addCleanup(stack.close)
        stack.enter_context(patch.object(owner, "decode", return_value=document))
        construct = stack.enter_context(patch.object(owner, "construct", return_value=document))
        stack.enter_context(patch.object(owner, "generation", return_value={"feed_public_key": "fixture"}))
        stack.enter_context(patch.object(owner, "check"))
        verify = stack.enter_context(patch.object(owner, "verify"))
        arguments = (root, {"mode": "rehearsal"}, Path("catalog"), Path("updater"), Path("artifacts"))
        return document, construct, verify, arguments

    def test_both_immutable_files_are_verified_with_fresh_admission_before_and_after(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            _, construct, verify, arguments = self.fixture(root)
            backend = Mock()
            result = owner.publish(*arguments, authorize=Mock(), backend=backend, registry={})
            self.assertEqual(construct.call_count, 5)
            self.assertEqual(verify.call_count, 5)
            self.assertEqual([call.args[0] for call in backend.put.call_args_list],
                             ["releases/v1.2.3/distribution.v1.json", "releases/v1.2.3/distribution.v1.json.sig"])
            self.assertEqual(backend.verify.call_count, 2)
            self.assertEqual(result["distribution_sha256"], hashlib.sha256((root / "distribution.v1.json").read_bytes()).hexdigest())
            def mutate(*args, **kwargs):
                (root / "distribution.v1.json").write_bytes(b"changed")
            backend.put.side_effect = mutate
            with self.assertRaisesRegex(ValueError, "freshly admitted"):
                owner.publish(*arguments, authorize=Mock(), backend=backend, registry={})

    def test_notification_binds_independent_updater_and_distribution_digests(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            _, construct, _, arguments = self.fixture(root)
            payload = owner.notification(*arguments, authorize=Mock(), manifest_sha256="a" * 64, registry={},
                                         bad_distribution_sha256="b" * 64, bad_version="1.2.4", backend=Mock())
            self.assertEqual(payload["distribution_sha256"], hashlib.sha256((root / "distribution.v1.json").read_bytes()).hexdigest())
            self.assertEqual(payload["bad_distribution_sha256"], "b" * 64)
            self.assertEqual(set(payload), {"channel", "bad_version", "last_good_version", "manifest_sha256", "distribution_sha256", "bad_distribution_sha256"})
            self.assertEqual(payload["manifest_sha256"], "a" * 64)
            self.assertEqual(construct.call_count, 2)
            with self.assertRaisesRegex(ValueError, "updater digest"):
                owner.notification(*arguments, authorize=Mock(), manifest_sha256="c" * 64, registry={}, backend=Mock())
            with self.assertRaisesRegex(ValueError, "bad distribution"):
                owner.notification(*arguments, authorize=Mock(), manifest_sha256="a" * 64, registry={},
                                   bad_distribution_sha256="invalid", backend=Mock())

    def test_failed_crypto_cannot_upload_or_dispatch(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            _, _, verify, arguments = self.fixture(root)
            verify.side_effect = ValueError("signature refused")
            backend = Mock()
            with self.assertRaisesRegex(ValueError, "signature refused"):
                owner.publish(*arguments, authorize=Mock(), backend=backend, registry={})
            backend.put.assert_not_called()

    def test_local_admission_alone_cannot_create_a_public_notification(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            _, _, _, arguments = self.fixture(root)
            with self.assertRaisesRegex(ValueError, "public byte verification"):
                owner.notification(*arguments, authorize=Mock(), manifest_sha256="a" * 64, registry={})
            backend = Mock()
            backend.verify.side_effect = ValueError("public object absent")
            with self.assertRaisesRegex(ValueError, "public object absent"):
                owner.notification(*arguments, authorize=Mock(), manifest_sha256="a" * 64, registry={}, backend=backend)

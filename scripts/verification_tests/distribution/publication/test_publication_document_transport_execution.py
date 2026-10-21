"""Immutable metadata and notifications recheck live authority rather than saved receipts."""
import hashlib
from pathlib import Path
import shutil
import tempfile
import unittest

from scripts.linux_release.publication.document import document_publication as owner
from verification_tests.distribution.publication import signed_release as fixture

ADMITTED = lambda binding: True


class DocumentTransportTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root, arguments = fixture.inputs(Path(temporary.name) / "release")
        self.arguments = (self.root, *arguments)
        self.signer = fixture.minisign()
        self.calls = self.signer.__enter__()
        self.addCleanup(self.signer.__exit__, None, None, None)

    def publish(self, backend, authorize=ADMITTED):
        return owner.publish(*self.arguments, authorize=authorize, backend=backend)

    def notify(self, backend, **keywords):
        return owner.notification(*self.arguments, authorize=ADMITTED, backend=backend,
                                  **{"manifest_sha256": fixture.manifest_sha256(), **keywords})

    def test_committed_signed_document_is_published_byte_for_byte_after_fresh_construction(self):
        backend = fixture.FixtureOrigin()
        admitted = self.publish(backend)
        self.assertEqual(backend.objects, {key: raw for key, raw in fixture.stored().items() if key.startswith("releases/")})
        self.assertEqual(admitted["distribution_sha256"],
                         hashlib.sha256((fixture.FIXTURE / fixture.DOCUMENT).read_bytes()).hexdigest())
        self.assertTrue(self.calls)

    def test_document_changed_during_upload_refuses_the_second_object(self):
        def mutate(key):
            (self.root / fixture.DOCUMENT).write_bytes((fixture.MISMATCH / fixture.DOCUMENT).read_bytes())
        backend = fixture.FixtureOrigin(mutate=mutate)
        with self.assertRaisesRegex(ValueError, "freshly admitted"):
            self.publish(backend)
        self.assertNotIn("releases/v1.2.3/" + fixture.SIGNATURE, backend.objects)

    def test_tampered_document_and_refused_campaign_never_reach_storage(self):
        backend = fixture.FixtureOrigin()
        with self.assertRaisesRegex(ValueError, "refused distribution facts"):
            self.publish(backend, authorize=lambda binding: False)
        shutil.copyfile(fixture.MISMATCH / fixture.DOCUMENT, self.root / fixture.DOCUMENT)
        with self.assertRaisesRegex(ValueError, "differs from freshly admitted"):
            self.publish(backend)
        self.assertEqual(backend.objects, {})

    def test_mismatched_or_unverifiable_signatures_never_reach_storage(self):
        for name, message in (("wrong-file.sig", "bound to 'latest.json'"), ("wrong-version.sig", "version 1.2.4"),
                              ("foreign-signer.sig", "registered feed key"),
                              ("unverifiable.sig", "^feed signature verification failed$")):
            with self.subTest(signature=name):
                shutil.copyfile(fixture.MISMATCH / name, self.root / fixture.SIGNATURE)
                backend = fixture.FixtureOrigin()
                with self.assertRaisesRegex(ValueError, message):
                    self.publish(backend)
                self.assertEqual(backend.objects, {})

    def test_notification_requires_public_bytes_and_binds_independent_digests(self):
        with self.assertRaisesRegex(ValueError, "public byte verification"):
            self.notify(None)
        with self.assertRaisesRegex(ValueError, "public object absent"):
            self.notify(fixture.FixtureOrigin())
        backend = fixture.FixtureOrigin()
        admitted = self.publish(backend)
        payload = self.notify(backend)
        self.assertEqual(payload, {"version": "1.2.3", "tag": "v1.2.3", "channel": "stable",
                                   "manifest_sha256": fixture.manifest_sha256(),
                                   "distribution_sha256": admitted["distribution_sha256"]})
        with self.assertRaisesRegex(ValueError, "updater digest differs"):
            self.notify(backend, manifest_sha256="c" * 64)

    def test_halt_notification_carries_the_explicit_bad_identity(self):
        backend = fixture.FixtureOrigin()
        admitted = self.publish(backend)
        payload = self.notify(backend, bad_distribution_sha256="b" * 64, bad_version="1.2.4")
        self.assertEqual(payload, {"channel": "stable", "bad_version": "1.2.4", "last_good_version": "1.2.3",
                                   "manifest_sha256": fixture.manifest_sha256(),
                                   "distribution_sha256": admitted["distribution_sha256"],
                                   "bad_distribution_sha256": "b" * 64})
        for keywords, message in (({"bad_distribution_sha256": "invalid", "bad_version": "1.2.4"}, "bad distribution digest"),
                                  ({"bad_distribution_sha256": "b" * 64, "bad_version": "1.2.3"}, "must differ"),
                                  ({"bad_version": "1.2.4"}, "independent bad distribution")):
            with self.subTest(keywords=keywords), self.assertRaisesRegex(ValueError, message):
                self.notify(backend, **keywords)

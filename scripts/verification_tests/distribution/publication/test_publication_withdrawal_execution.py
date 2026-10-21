"""Halt payloads retain independent bad/good identities and recheck before effects."""
import os
import unittest
from unittest.mock import patch

import release_withdrawal as owner
from scripts.linux_release.publication.document import withdrawal
from verification_tests.distribution.publication import signed_release as fixture

BAD, GOOD = fixture.FIXTURE, fixture.PREVIOUS


class DistributionWithdrawalTests(unittest.TestCase):
    def setUp(self):
        signer = fixture.minisign()
        signer.__enter__()
        self.addCleanup(signer.__exit__, None, None, None)

    def test_committed_signed_releases_are_authenticated_before_their_digests_escape(self):
        with fixture.storage({**fixture.stored(BAD), **fixture.stored(GOOD)}) as objects:
            self.assertEqual(withdrawal.digests("1.2.3", "1.2.2", fixture.manifest_sha256(GOOD)),
                             {"distribution_sha256": fixture.distribution_sha256(GOOD),
                              "bad_distribution_sha256": fixture.distribution_sha256(BAD)})
            self.assertEqual(withdrawal.digests("1.2.3", None, None),
                             {"distribution_sha256": None, "bad_distribution_sha256": fixture.distribution_sha256(BAD)})
            with self.assertRaisesRegex(ValueError, "replacement updater"):
                withdrawal.digests("1.2.3", "1.2.2", fixture.manifest_sha256(BAD))
            del objects["releases/v1.2.3/distribution.v1.json.sig"]
            with self.assertRaisesRegex(ValueError, "every selected"):
                withdrawal.digests("1.2.3", None, None)

    def test_stored_document_or_signature_mismatch_refuses(self):
        for changes, message in (({"document": fixture.MISMATCH / fixture.DOCUMENT}, "^feed signature verification failed$"),
                                 ({"signature": fixture.MISMATCH / "wrong-version.sig"}, "version 1.2.4"),
                                 ({"signature": fixture.MISMATCH / "foreign-signer.sig"}, "registered feed key")):
            with self.subTest(changes=changes), fixture.storage(fixture.stored(BAD, **changes)), \
                    self.assertRaisesRegex(ValueError, message):
                withdrawal.digests("1.2.3", None, None)

    def test_apply_rechecks_stored_identities_before_any_external_effect_and_sends_both(self):
        row = {"channel": "stable", "bad": "1.2.3", "last_good": None, "manifest_sha256": None,
               "distribution_sha256": None, "bad_distribution_sha256": "a" * 64,
               "change_tap": False, "sha": None, "content": None, "path": "ignored"}
        prepared = {"bad_versions": [], "channels": [row]}
        dispatched = []
        def api(token, path, method="GET", body=None):
            dispatched.append(body)
        with fixture.storage(fixture.stored(BAD)), patch.object(owner, "api", side_effect=api), \
                patch.object(owner, "wait_for_withdrawal"), patch.dict(os.environ, {"WWW_DISPATCH_TOKEN": "fixture"}):
            with self.assertRaisesRegex(ValueError, "changed after preparation"):
                owner.apply(prepared)
            self.assertEqual(dispatched, [])
            row["bad_distribution_sha256"] = fixture.distribution_sha256(BAD)
            owner.apply(prepared)
        payload = dispatched[-1]["client_payload"]
        self.assertIsNone(payload["distribution_sha256"])
        self.assertEqual(payload["bad_distribution_sha256"], fixture.distribution_sha256(BAD))

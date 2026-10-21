"""Halt payloads retain independent bad/good identities and recheck before effects."""
import hashlib
import unittest
from unittest.mock import patch
import release_withdrawal as owner
from scripts.linux_release.publication.document import withdrawal

class DistributionWithdrawalTests(unittest.TestCase):
    def test_authenticated_documents_and_updaters_are_bound_before_hashes_escape(self):
        data = {"releases/v1.2.3/distribution.v1.json": b"bad document",
                "releases/v1.2.3/distribution.v1.json.sig": b"bad signature",
                "updates/releases/1.2.3.json": b"bad updater",
                "releases/v1.2.2/distribution.v1.json": b"good document",
                "releases/v1.2.2/distribution.v1.json.sig": b"good signature",
                "updates/releases/1.2.2.json": b"good updater"}
        expected = hashlib.sha256(b"good updater").hexdigest()
        with patch("scripts.release_distribution.read_storage_bytes", side_effect=lambda key: data.get(key)), patch.object(withdrawal, "verify_metadata") as verify:
            result = withdrawal.digests("1.2.3", "1.2.2", expected)
            self.assertEqual(verify.call_count, 2)
            self.assertEqual(result, {"distribution_sha256": hashlib.sha256(b"good document").hexdigest(),
                                      "bad_distribution_sha256": hashlib.sha256(b"bad document").hexdigest()})
            with self.assertRaisesRegex(ValueError, "replacement updater"):
                withdrawal.digests("1.2.3", "1.2.2", "a" * 64)
            del data["releases/v1.2.3/distribution.v1.json.sig"]
            with self.assertRaisesRegex(ValueError, "every selected"):
                withdrawal.digests("1.2.3", None, None)

    def test_apply_rechecks_digests_before_any_external_effect_and_sends_both(self):
        row = {"channel": "stable", "bad": "1.2.3", "last_good": None, "manifest_sha256": None,
               "distribution_sha256": None, "bad_distribution_sha256": "a" * 64,
               "change_tap": False, "sha": None, "content": None, "path": "ignored"}
        prepared = {"bad_versions": [], "channels": [row]}
        with patch.object(withdrawal, "digests", return_value={"distribution_sha256": None, "bad_distribution_sha256": "b" * 64}), patch.object(owner, "api") as api:
            with self.assertRaisesRegex(ValueError, "changed after preparation"):
                owner.apply(prepared)
            api.assert_not_called()
        with patch.object(withdrawal, "digests", return_value={"distribution_sha256": None, "bad_distribution_sha256": "a" * 64}), patch.dict(owner.os.environ, {"WWW_DISPATCH_TOKEN": "fixture"}), patch.object(owner, "api") as api, patch.object(owner, "wait_for_withdrawal"):
            owner.apply(prepared)
            payload = api.call_args.args[3]["client_payload"]
            self.assertIsNone(payload["distribution_sha256"])
            self.assertEqual(payload["bad_distribution_sha256"], "a" * 64)

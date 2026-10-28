"""Credentialed cache rehearsals cannot adopt or clean another run's objects."""
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch
import urllib.error

from scripts.nix_cache.r2 import R2Transport
from scripts.nix_cache.rehearsal_r2 import RehearsalTransport, execute, warm_missing


class LiveCacheBoundariesTests(unittest.TestCase):
    def transport(self):
        value = RehearsalTransport.__new__(RehearsalTransport)
        value.prefix = "release-system-tests/fixture/nix-cache"
        value.created = {"nar/owned.nar.xz", "publication-lock.json"}
        value._aws = Mock(return_value="")
        value.listing = Mock(return_value=[])
        return value

    def test_cleanup_deletes_recorded_keys_and_verifies_complete_absence(self):
        backend = self.transport()
        backend.cleanup()
        self.assertEqual({call.args for call in backend._aws.call_args_list}, {
            ("delete-object", "--key", backend.prefix + "/nar/owned.nar.xz"),
            ("delete-object", "--key", backend.prefix + "/publication-lock.json")})
        backend.listing.assert_called_once_with()
        backend.listing.return_value = [{"name": "unrecorded"}]
        with self.assertRaisesRegex(ValueError, "exact-key cleanup failed"):
            backend.cleanup()
        self.assertTrue(all("unrecorded" not in call.args[-1] for call in backend._aws.call_args_list))

    def test_cleanup_attempts_all_recorded_keys_but_cannot_report_transport_failure_as_absence(self):
        backend = self.transport()
        backend._aws.side_effect = [ValueError("unavailable"), ""]
        with self.assertRaisesRegex(ValueError, "cleanup failed"):
            backend.cleanup()
        self.assertEqual(backend._aws.call_count, 2)

    def test_nonempty_prefix_refuses_before_public_io_or_publication(self):
        fixture = SimpleNamespace(report={"request": {"mode": "rehearsal"}})
        backend = self.transport()
        backend.listing.return_value = [{"name": "another-run"}]
        with patch("scripts.nix_cache.rehearsal_r2.warm_missing") as public, \
                patch("scripts.nix_cache.rehearsal_r2.publish") as publish:
            with self.assertRaisesRegex(ValueError, "refusing to adopt"):
                execute(fixture, backend)
            public.assert_not_called()
            publish.assert_not_called()
            backend._aws.assert_not_called()

    def test_production_refuses_before_transport_initialization(self):
        with patch.object(R2Transport, "__init__") as initialize:
            with self.assertRaisesRegex(ValueError, "cannot address production"):
                RehearsalTransport({"mode": "production"})
            initialize.assert_not_called()

    def test_missing_public_probe_requires_exact_404_and_ordinary_url(self):
        backend = SimpleNamespace(public="https://test.example/release-system-tests/fixture/nix-cache")
        opener = Mock()
        with patch("scripts.nix_cache.rehearsal_r2.urllib.request.build_opener", return_value=opener):
            opener.open.side_effect = urllib.error.HTTPError("https://test.example", 403, "refused", {}, None)
            with self.assertRaisesRegex(ValueError, "did not return a missing object"):
                warm_missing(backend, "path.narinfo")
            opener.open.side_effect = urllib.error.HTTPError("https://test.example", 404, "missing", {}, None)
            warm_missing(backend, "path.narinfo")
            self.assertEqual(opener.open.call_args.args[0].full_url, backend.public + "/path.narinfo")

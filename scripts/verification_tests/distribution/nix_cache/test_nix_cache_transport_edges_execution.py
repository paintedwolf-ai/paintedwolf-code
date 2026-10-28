"""The local cache rehearsal keeps immutable bytes and tests ordinary HTTP reads."""

from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import urllib.error
import urllib.request

from scripts.nix_cache import rehearsal_transport


class NixCacheTransportEdges(unittest.TestCase):
    def test_fixture_store_preserves_immutable_bytes_and_checks_rotation(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "objects"
            root.mkdir()
            store = rehearsal_transport.FixtureStore(root, {})
            record = {"content_type": "application/octet-stream", "cache_control": "no-store"}
            source = Path(temporary) / "source"
            source.write_bytes(b"selected")
            with store.serialized("publish"):
                store.put("nar/selected.nar.xz", source, record)
                store.put("nar/selected.nar.xz", source, record)
                self.assertEqual(store.read("nar/selected.nar.xz"), b"selected")
                with self.assertRaisesRegex(ValueError, "immutable"):
                    store.put_bytes("nar/selected.nar.xz", b"changed", record["content_type"], record["cache_control"])
                with self.assertRaisesRegex(ValueError, "rotation metadata changed"):
                    store.replace("nar/selected.nar.xz", source, record, b"stale")
                source.write_bytes(b"rotated")
                store.replace("nar/selected.nar.xz", source, record, b"selected")
            self.assertEqual(store.read("nar/selected.nar.xz"), b"rotated")
            rows = store.listing()
            self.assertEqual([row["name"] for row in rows], ["nar/selected.nar.xz"])
            store.delete(rows[0])
            self.assertIsNone(store.read("nar/selected.nar.xz"))

    def test_rehearsal_refuses_cached_absence_then_retries_and_collects_only_orphans(self):
        request = {"mode": "rehearsal", "cache_prefix": "release-system-tests/fixture/nix-cache",
                   "version": "0.0.0-rehearsal", "system": "x86_64-linux"}
        report = {"request": request, "closure": {"selected": {}},
                  "objects": [{"name": "selected.narinfo", "phase": "narinfo"},
                              {"name": "nar/selected.nar.xz", "phase": "nar"}]}
        attempts = []

        def publish(_cache, selected, *, authorize, backend_factory):
            store = backend_factory(selected["request"])
            self.assertTrue(authorize(selected["request"], selected))
            attempts.append(selected)
            if len(attempts) == 1:
                with self.assertRaises(urllib.error.HTTPError) as missing:
                    urllib.request.urlopen(store.public + "/selected.narinfo")
                self.assertEqual(missing.exception.code, 404)
                raise ValueError("negative cache response refused")
            for row in selected["objects"]:
                store.put_bytes(row["name"], b"signed cache bytes", "application/octet-stream", "no-store")
            with urllib.request.urlopen(store.public + "/selected.narinfo") as response:
                self.assertEqual(response.read(), b"signed cache bytes")

        def collect(_request, _cutoff, *, authorize, backend_factory):
            self.assertTrue(authorize(_request, _cutoff))
            store = backend_factory(_request)
            row = {"name": "nar/orphan.nar.xz"}
            store.delete(row)
            return [row["name"]]

        with tempfile.TemporaryDirectory() as temporary:
            cache = Path(temporary) / "cache"
            cache.mkdir()
            with patch.object(rehearsal_transport, "publish", side_effect=publish), \
                 patch.object(rehearsal_transport, "inventory", return_value=report["objects"]), \
                 patch.object(rehearsal_transport, "withdraw"), \
                 patch.object(rehearsal_transport, "collect", side_effect=collect):
                cases = rehearsal_transport.run(cache, report, cache, ["fixture-public-key"])
        self.assertEqual(len(attempts), 4)
        self.assertIn("negative-cache-refusal", cases)
        self.assertIn("unreferenced-debris-gc", cases)

    def test_production_request_never_creates_a_local_http_fixture(self):
        with patch.object(rehearsal_transport, "ThreadingHTTPServer") as server:
            with self.assertRaisesRegex(ValueError, "refuses production"):
                rehearsal_transport.run(Path("/unused"), {"request": {"mode": "production"}}, Path("/unused"), [])
        server.assert_not_called()


if __name__ == "__main__":
    unittest.main()

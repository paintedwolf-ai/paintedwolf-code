"""R2 cache reads and deletes honor the exact observed object; rehearsal keeps locks, substitution,
and cleanup observable."""

import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest import TestCase
from unittest.mock import Mock
from unittest.mock import patch
import urllib.error

from scripts.nix_cache import rehearsal_r2
from scripts.nix_cache.r2 import R2Transport


OUTPUT = "/nix/store/" + "0" * 32 + "-application"
DEPENDENCY = "/nix/store/" + "1" * 32 + "-dependency"


class RemoteCacheRehearsalEdges(unittest.TestCase):
    def test_production_request_cannot_construct_a_rehearsal_transport(self):
        with self.assertRaisesRegex(ValueError, "cannot address production"):
            rehearsal_r2.RehearsalTransport({"mode": "production"})

    def test_missing_public_metadata_requires_an_exact_404(self):
        backend = SimpleNamespace(public="https://example.invalid")

        class Missing:
            def open(self, _request, timeout):
                self.assert_timeout = timeout
                raise urllib.error.HTTPError("https://example.invalid", 404, "missing", {}, None)

        with patch.object(rehearsal_r2.urllib.request, "build_opener", return_value=Missing()):
            rehearsal_r2.warm_missing(backend, "selected.narinfo")
        class Denied:
            def open(self, _request, timeout):
                raise urllib.error.HTTPError("https://example.invalid", 403, "denied", {}, None)
        with patch.object(rehearsal_r2.urllib.request, "build_opener", return_value=Denied()):
            with self.assertRaisesRegex(ValueError, "did not return a missing object"):
                rehearsal_r2.warm_missing(backend, "selected.narinfo")

    def test_cold_copy_requires_the_complete_dependency_closure(self):
        fixture = SimpleNamespace(work=Path("/fixture"), output=OUTPUT,
                                  report={"closure": {OUTPUT: {}, DEPENDENCY: {}}})
        backend = SimpleNamespace(public="https://example.invalid")
        with patch.object(rehearsal_r2, "local_store", return_value="local?root=fixture"), \
             patch.object(rehearsal_r2, "nix") as nix, \
             patch.object(rehearsal_r2, "path_info", return_value={OUTPUT: {}}):
            with self.assertRaisesRegex(ValueError, "complete dependent closure"):
                rehearsal_r2.cold_copy(fixture, backend, "cold", ["public"])
        self.assertEqual(nix.call_args.args[0], "copy")
        self.assertIn("--trusted-public-keys", nix.call_args.args)

    def test_execute_refuses_interruption_then_recovers_exact_lock_and_rotates(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            cache = work / "cache"
            cache.mkdir()
            (cache / "selected.narinfo").write_bytes(b"metadata")
            request = {"mode": "rehearsal", "version": "0.0.0-rehearsal", "system": "x86_64-linux"}
            objects = [{"name": "selected.narinfo", "phase": "narinfo"}]
            report = {"request": request, "closure": {OUTPUT: {"references": [DEPENDENCY]}, DEPENDENCY: {}},
                      "objects": objects, "public_keys": ["old-public"]}
            fixture = SimpleNamespace(work=work, cache=cache, report=report, output=OUTPUT,
                                      keys=["old-public", "new-public"], key_files=[work / "old", work / "new"])
            events = []
            state = {"lock": None, "withdrawn": False}

            class Backend:
                public = "https://example.invalid"

                def listing(self):
                    return []

                def read(self, _name):
                    return None

                def _read(self, _name):
                    return b"lock", "etag"

                def _write(self, _name, path, *_arguments):
                    state["lock"] = json.loads(path.read_text())["operation"]

                def recover_lock(self, operation):
                    if operation != state["lock"]:
                        raise ValueError("foreign lock")
                    state["lock"] = None

                def verify_public(self, name, record):
                    events.append(("public", name, record["phase"]))

            backend = Backend()
            publications = []

            def publish(_cache, _report, **_arguments):
                publications.append(_report)
                if len(publications) == 1:
                    raise ValueError("metadata upload interrupted")
                if state["lock"] is not None:
                    raise ValueError("publication lock held")
                if state["withdrawn"]:
                    raise ValueError("withdrawn")

            def cold_copy(_fixture, _backend, name, keys):
                events.append(("copy", name, tuple(keys)))
                if keys == ["new-public"] and name == "wrong-key-cold":
                    raise ValueError("wrong key")

            with patch.object(rehearsal_r2, "warm_missing"), \
                 patch.object(rehearsal_r2, "publish", side_effect=publish), \
                 patch.object(rehearsal_r2, "cold_copy", side_effect=cold_copy), \
                 patch.object(rehearsal_r2, "nix"), patch.object(rehearsal_r2, "verify"), \
                 patch.object(rehearsal_r2, "inventory", return_value=objects), \
                 patch.object(rehearsal_r2, "withdraw", side_effect=lambda *_args, **_kwargs: state.update(withdrawn=True)):
                cases = rehearsal_r2.execute(fixture, backend)
            self.assertIn("exact-lock-recovery", cases)
            self.assertIn(("copy", "rotated-cold", ("new-public",)), events)
            self.assertIn(("public", "selected.narinfo", "narinfo"), events)
            self.assertGreaterEqual(len(publications), 6)


class CacheR2ObjectEdges(TestCase):
    def test_read_checks_the_locked_inventory_size(self):
        transport = R2Transport.__new__(R2Transport)
        transport.prefix = "nix-cache"
        row = {"name": "release.narinfo", "bytes": 4, "etag": "observed"}
        transport.listing = Mock(return_value=[row])

        def get_object(*arguments):
            self.assertEqual(arguments[:3], ("get-object", "--key", "nix-cache/release.narinfo"))
            self.assertEqual(arguments[-2:], ("--if-match", "observed"))
            with open(arguments[3], "wb") as target:
                target.write(b"data")
            return json.dumps({"ETag": "observed"})

        transport._aws = Mock(side_effect=get_object)
        self.assertEqual(transport.read("release.narinfo"), b"data")
        self.assertEqual(transport._read("release.narinfo"), (b"data", "observed"))
        def short_object(*arguments):
            with open(arguments[3], "wb") as target:
                target.write(b"bad")
            return json.dumps({"ETag": "observed"})

        transport._aws.side_effect = short_object
        with self.assertRaisesRegex(ValueError, "changed while reading"):
            transport.read("release.narinfo")

    def test_delete_refuses_a_changed_inventory(self):
        transport = R2Transport.__new__(R2Transport)
        transport.prefix = "nix-cache"
        transport.listing = Mock(return_value=[{"name": "release.narinfo", "etag": "new"}])
        transport._aws = Mock()
        with self.assertRaisesRegex(ValueError, "changed after its locked inventory"):
            transport.delete({"name": "release.narinfo", "etag": "old"})
        transport._aws.assert_not_called()
        transport.delete({"name": "release.narinfo", "etag": "new"})
        transport._aws.assert_called_once_with("delete-object", "--key", "nix-cache/release.narinfo")


if __name__ == "__main__":
    unittest.main()

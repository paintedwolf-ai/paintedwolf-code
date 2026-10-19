"""R2 repository activation retains content and refuses stale or interrupted plans."""

import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release.repository_objects import encoded, immutable
from linux_release.repository_r2 import publish_r2


class RepositoryR2Tests(unittest.TestCase):
    def setUp(self):
        self.receipt = {"format_version": 1, "family": "deb", "channel": "preview",
                        "source_commit": "f" * 40, "version": "0.0.1", "rehearsal": True}
        self.objects = {}
        self.events = []
        self.fail = None
        self.reads = 0
        self.race = False

    def local(self, receipt, directory, store, origin, catalog, expected, rehearsal):
        identity = hashlib.sha256(encoded(receipt)).hexdigest()
        generation = store / "generations" / identity
        generation.mkdir(parents=True)
        (generation / "generation.json").write_bytes(encoded(receipt))
        (generation / "application.deb").write_bytes(b"selected package")
        channel = store / "channels/deb/preview"
        for name in ["pool/hash/application.deb", "dists/preview/main/by-hash/SHA256/hash",
                     "dists/preview/main/Packages", "dists/preview/InRelease"]:
            path = channel / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(name.encode())
        return {"generation": identity, "version": receipt["version"], "rehearsal": True}

    def current(self, key, directory):
        self.reads += 1
        if self.race and self.reads == 2:
            return {"generation": "other publisher", "version": "0.0.2", "rehearsal": True}
        return json.loads(self.objects[key]) if key in self.objects else None

    def put(self, key, source, mutable=False):
        self.events.append(("put", key, mutable))
        if self.fail and self.fail in key:
            raise OSError("interrupted upload")
        if not mutable and key in self.objects and self.objects[key] != source.read_bytes():
            raise ValueError("immutable collision")
        self.objects[key] = source.read_bytes()

    def public(self, url, source, mutable=False):
        key = url.removeprefix("https://repository.example.invalid/")
        self.events.append(("readback", key, mutable))
        if self.objects.get(key) != source.read_bytes():
            raise ValueError("public bytes differ")

    def publish(self, expected=None, rehearsal=True, prefix="release-system-tests/native-contract"):
        with tempfile.TemporaryDirectory() as temporary:
            with patch("linux_release.repository_r2.publish", side_effect=self.local), \
                    patch("linux_release.repository_r2.current", side_effect=self.current), \
                    patch("linux_release.repository_r2.put", side_effect=self.put), \
                    patch("linux_release.repository_r2.public_bytes", side_effect=self.public):
                return publish_r2(self.receipt, Path(temporary), Path(temporary) / "keys.json", prefix,
                                  "https://repository.example.invalid", expected, rehearsal)

    def test_content_and_readback_precede_the_single_apt_authority(self):
        result = self.publish()
        writes = [key for event, key, _ in self.events if event == "put"]
        self.assertTrue(writes[-2].endswith("/InRelease"))
        self.assertTrue(writes[-1].endswith("/current.json"))
        authority = next(index for index, item in enumerate(self.events) if item[1].endswith("/InRelease"))
        self.assertTrue(any(event == "readback" and "/generations/" in key
                            for event, key, _ in self.events[:authority]))
        self.assertTrue(result["public_verified"])
        self.assertTrue(all(not mutable for _, key, mutable in self.events if "/generations/" in key))
        # A retry after successful activation is safe with the originally reviewed predecessor.
        self.assertEqual(self.publish()["generation"], result["generation"])

    def test_interrupted_upload_never_selects_partial_content_and_retry_recovers(self):
        self.fail = "Packages"
        with self.assertRaises(OSError):
            self.publish()
        self.assertFalse(any(key.endswith(("/InRelease", "/current.json")) for key in self.objects))
        self.fail = None
        self.assertTrue(self.publish()["public_verified"])

    def test_stale_and_concurrently_changed_plans_never_activate(self):
        with self.assertRaises(ValueError):
            self.publish("0" * 64)
        self.assertFalse(self.events)
        self.race = True
        self.reads = 0
        with self.assertRaises(ValueError):
            self.publish()
        self.assertFalse(any(key.endswith(("/InRelease", "/current.json")) for key in self.objects))

    def test_fixture_namespace_and_production_admission_are_checked_before_storage(self):
        for prefix in ["linux/native", "release-system-tests/../linux", "/release-system-tests/run"]:
            with self.subTest(prefix=prefix), self.assertRaises(ValueError):
                self.publish(prefix=prefix)
        with self.assertRaises(ValueError):
            self.publish(rehearsal=False)
        self.assertFalse(self.events)

    def test_immutable_local_origin_preserves_original_bytes_on_collision(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "selected"
            immutable(path, b"selected")
            immutable(path, b"selected")
            with self.assertRaises(ValueError):
                immutable(path, b"changed")
            self.assertEqual(path.read_bytes(), b"selected")

"""Pacman R2 publication binds signatures before databases and retains package bytes."""

import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release.pacman_r2 import publish_r2
from linux_release.repository_objects import encoded


class PacmanPublicationTests(unittest.TestCase):
    def setUp(self):
        self.receipt = {"format_version": 1, "channel": "preview", "architecture": "x86_64",
                        "version": "0.0.1", "source_commit": "f" * 40, "rehearsal": True,
                        "repository": "painted-wolf-code-preview", "family": "pacman",
                        "signing_generation": 1, "key_fingerprint": "A" * 40, "inventory_sha256": "b" * 64,
                        "packages": [{"native_version": "0.0.1-1"}],
                        "objects": [{"path": "application.pkg.tar.zst"}, {"path": "application.pkg.tar.zst.sig"}]}
        self.objects, self.events = {}, []
        self.fail_suffix, self.race, self.reads = None, False, 0

    def local(self, receipt, directory, store, origin, catalog, expected, rehearsal):
        identity = hashlib.sha256(encoded(receipt)).hexdigest()
        generation = store / "generations" / identity
        generation.mkdir(parents=True)
        (generation / "generation.json").write_bytes(encoded(receipt))
        channel = store / "channels/pacman/preview/x86_64"
        channel.mkdir(parents=True)
        names = [row["path"] for row in receipt["objects"]]
        for suffix in ["db", "files"]:
            names += [receipt["repository"] + ending for ending in
                      [f".{suffix}.sig", f".{suffix}.tar.gz.sig", f".{suffix}", f".{suffix}.tar.gz"]]
        for name in names:
            (generation / name).write_bytes(name.encode())
            (channel / name).write_bytes(name.encode())
        return {"generation": identity, "version": receipt["version"], "packages": receipt["packages"],
                "rehearsal": True}

    def current(self, key, directory):
        if key.endswith("current.json"):
            self.reads += 1
        if self.race and self.reads == 2:
            return {"generation": "other"}
        return json.loads(self.objects[key]) if key in self.objects else None

    def put(self, key, source, mutable=False):
        self.events.append((key, mutable))
        if self.fail_suffix and "/channels/" in key and key.endswith(self.fail_suffix):
            raise OSError("interrupted activation")
        if not mutable and key in self.objects and self.objects[key] != source.read_bytes():
            raise ValueError("immutable collision")
        self.objects[key] = source.read_bytes()

    def public(self, url, source, mutable=False):
        key = url.removeprefix("https://repository.example.invalid/")
        if self.objects.get(key) != source.read_bytes():
            raise ValueError("public bytes differ")

    def publish(self, expected=None, rehearsal=True):
        with tempfile.TemporaryDirectory() as temporary:
            with patch("linux_release.pacman_r2.publish", side_effect=self.local), \
                    patch("linux_release.pacman_r2.current", side_effect=self.current), \
                    patch("linux_release.pacman_r2.put", side_effect=self.put), \
                    patch("linux_release.pacman_r2.public_bytes", side_effect=self.public), \
                    patch("linux_release.pacman_r2.subprocess.check_output", return_value="0"):
                return publish_r2(self.receipt, Path(temporary), Path(temporary) / "keys.json",
                                  "release-system-tests/pacman-contract", "https://repository.example.invalid",
                                  expected, rehearsal)

    def test_packages_and_signatures_precede_database_alias_and_state_is_last(self):
        result = self.publish()
        writes = [key for key, _ in self.events if "/channels/" in key]
        db = next(index for index, key in enumerate(writes) if key.endswith("/painted-wolf-code-preview.db"))
        signature = next(index for index, key in enumerate(writes) if key.endswith("/painted-wolf-code-preview.db.sig"))
        self.assertLess(signature, db)
        self.assertTrue(writes[-1].endswith("/current.json"))
        self.assertTrue(all(not mutable for key, mutable in self.events if "/generations/" in key))
        self.assertTrue(result["public_verified"])
        self.assertEqual(self.publish()["generation"], result["generation"])

    def test_interrupted_pair_upload_preserves_objects_and_retry_finishes(self):
        self.fail_suffix = "/painted-wolf-code-preview.db.sig"
        with self.assertRaises(OSError):
            self.publish()
        self.assertTrue(any(key.endswith("application.pkg.tar.zst") for key in self.objects if "/channels/" in key))
        self.assertFalse(any(key.endswith(("/painted-wolf-code-preview.db", "/current.json")) for key in self.objects if "/channels/" in key))
        self.fail_suffix = None
        self.assertTrue(self.publish()["public_verified"])

    def test_stale_or_concurrently_changed_plan_never_selects_database(self):
        with self.assertRaises(ValueError):
            self.publish("0" * 64)
        self.assertFalse(self.events)
        self.race, self.reads = True, 0
        with self.assertRaises(ValueError):
            self.publish()
        self.assertFalse(any(key.endswith(("/painted-wolf-code-preview.db", "/current.json")) for key in self.objects if "/channels/" in key))

    def test_production_path_requires_controller_admission(self):
        with self.assertRaises(ValueError):
            self.publish(rehearsal=False)
        self.assertFalse(self.events)

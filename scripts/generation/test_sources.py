"""Pinned source and patch publication contracts."""

import hashlib
import io
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest import mock

import sources


class SourceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        archive = self.root / "source.tar"
        with tarfile.open(archive, "w") as output:
            member = tarfile.TarInfo("root/value")
            member.size = 4
            output.addfile(member, io.BytesIO(b"old\n"))
        self.pin = {"url": archive.as_uri(), "sha256": hashlib.sha256(archive.read_bytes()).hexdigest()}
        self.cache = self.root / "cache"
        patch = mock.patch.object(sources, "__file__", str(self.root / "sources.py"))
        patch.start()
        self.addCleanup(patch.stop)

    def add_patch(self, original="old"):
        path = self.root / "change.patch"
        path.write_text(f"--- a/value\n+++ b/value\n@@ -1 +1 @@\n-{original}\n+new\n")
        self.pin["patches"] = [{"path": path.name, "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}]
        return path

    def test_patch_uses_separate_cache_and_preserves_original_source(self):
        original = sources.ensure_source(self.pin, self.cache)
        self.add_patch()
        patched = sources.ensure_source(self.pin, self.cache)
        self.assertNotEqual(original, patched)
        self.assertEqual((original / "value").read_text(), "old\n")
        self.assertEqual((patched / "value").read_text(), "new\n")
        self.assertEqual(sources.ensure_source(self.pin, self.cache), patched)

    def test_changed_patch_is_rejected_even_when_source_is_cached(self):
        patch = self.add_patch()
        sources.ensure_source(self.pin, self.cache)
        patch.write_text("changed\n")
        with self.assertRaisesRegex(ValueError, "patch digest mismatch"):
            sources.ensure_source(self.pin, self.cache)

    def test_failed_patch_never_publishes_source(self):
        self.add_patch("mismatch")
        destination = self.cache / sources.source_identity(self.pin)
        with self.assertRaises(subprocess.CalledProcessError):
            sources.ensure_source(self.pin, self.cache)
        self.assertFalse(destination.exists())


if __name__ == "__main__":
    unittest.main()

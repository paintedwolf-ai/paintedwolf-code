"""Isolated compiler publication contracts."""

import io
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import swift_toolchain


class SwiftToolchainTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.cache = Path(self.temporary.name)

    def test_signature_failure_never_extracts_or_publishes(self):
        with patch.object(swift_toolchain.shutil, "disk_usage", return_value=type("Disk", (), {"free": 20 << 30})()), \
             patch.object(swift_toolchain.urllib.request, "urlopen", return_value=io.BytesIO(b"package")), \
             patch.object(swift_toolchain.subprocess, "run", side_effect=subprocess.CalledProcessError(1, "pkgutil")) as run:
            with self.assertRaises(subprocess.CalledProcessError):
                swift_toolchain.prepare(self.cache)
        self.assertEqual(run.call_count, 1)
        self.assertEqual(run.call_args.args[0][1], "--check-signature")
        self.assertFalse(swift_toolchain.executable(self.cache).exists())
        self.assertEqual(list(self.cache.iterdir()), [])

    def test_verified_toolchain_is_reused_without_another_download(self):
        def package_tool(command, **_):
            if command[1] == "--expand-full":
                compiler = Path(command[3]) / "Swift.pkg/Payload/usr/bin/swift"
                compiler.parent.mkdir(parents=True)
                compiler.write_bytes(b"verified compiler")
        with patch.object(swift_toolchain.shutil, "disk_usage", return_value=type("Disk", (), {"free": 20 << 30})()), \
             patch.object(swift_toolchain.urllib.request, "urlopen", return_value=io.BytesIO(b"package")) as download, \
             patch.object(swift_toolchain.subprocess, "run", side_effect=package_tool):
            first = swift_toolchain.prepare(self.cache)
            second = swift_toolchain.prepare(self.cache)
        self.assertEqual(first, second)
        self.assertEqual(first.read_bytes(), b"verified compiler")
        self.assertTrue((first.parents[2] / "source.json").is_file())
        self.assertEqual(download.call_count, 1)

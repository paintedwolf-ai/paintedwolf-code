"""Isolated compiler publication contracts."""

import io
import hashlib
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
        environment = patch.dict(swift_toolchain.os.environ)
        environment.start()
        self.addCleanup(environment.stop)
        swift_toolchain.os.environ.pop("PW_GENERATION_SWIFT", None)
        digest = patch.object(swift_toolchain, "EXPECTED_SHA256", hashlib.sha256(b"package").hexdigest())
        digest.start()
        self.addCleanup(digest.stop)

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

    def test_explicit_compiler_requires_absolute_executable(self):
        for value in ("swift", str(self.cache / "absent")):
            with patch.dict(swift_toolchain.os.environ, {"PW_GENERATION_SWIFT": value}):
                with self.assertRaisesRegex(ValueError, "absolute executable"):
                    swift_toolchain.executable(self.cache)
        compiler = self.cache / "swift"
        compiler.write_text("compiler")
        compiler.chmod(0o755)
        with patch.dict(swift_toolchain.os.environ, {"PW_GENERATION_SWIFT": str(compiler)}), \
             patch.object(swift_toolchain.subprocess, "run") as run:
            self.assertEqual(swift_toolchain.prepare(self.cache), compiler)
            self.assertEqual(swift_toolchain.executable(self.cache), compiler)
            run.assert_called_once_with([str(compiler), "--version"], check=True)

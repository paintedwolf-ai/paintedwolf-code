"""SDK extraction retains only bounded compiler inputs."""

import hashlib
import io
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import vulkan_tools


class VulkanToolTests(unittest.TestCase):
    def test_extracts_compiler_and_headers_without_loader(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive = root / "sdk.tar"
            with tarfile.open(archive, "w") as bundle:
                for name in ("bin/glslc", "include/vulkan/vulkan.h", "share/cmake/SPIRV-Headers/SPIRV-HeadersConfig.cmake",
                             "lib/libvulkan.so"):
                    entry = tarfile.TarInfo(f"{vulkan_tools.VERSION}/x86_64/{name}")
                    entry.size = 7
                    bundle.addfile(entry, io.BytesIO(b"fixture"))
            output = root / "tools"
            vulkan_tools.unpack(archive, output)
            self.assertEqual((output / "bin/glslc").read_bytes(), b"fixture")
            self.assertTrue((output / "bin/glslc").stat().st_mode & 0o100)
            self.assertFalse((output / "lib").exists())
            self.assertTrue((output / "share/cmake/SPIRV-Headers/SPIRV-HeadersConfig.cmake").is_file())

    def test_rejects_escape_and_selected_symlink(self):
        for name in ("../escape", f"{vulkan_tools.VERSION}/x86_64/bin/glslc"):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                archive = root / "sdk.tar"
                with tarfile.open(archive, "w") as bundle:
                    entry = tarfile.TarInfo(name)
                    entry.type = tarfile.SYMTYPE
                    entry.linkname = "/outside"
                    bundle.addfile(entry)
                with self.assertRaises(ValueError):
                    vulkan_tools.unpack(archive, root / "tools")
                self.assertFalse((root / "escape").exists())

    def test_prepare_reuses_verified_tools_and_downloads_atomically(self):
        with tempfile.TemporaryDirectory() as temporary:
            cache = Path(temporary)
            destination = cache / vulkan_tools.CACHE_NAME
            destination.mkdir()
            (destination / ".sha256").write_text(vulkan_tools.SHA256 + "\n")
            with patch.object(vulkan_tools.subprocess, "run") as run:
                self.assertEqual(vulkan_tools.prepare(cache), destination)
            run.assert_not_called()

            destination.rename(cache / "old-tools")
            archive_bytes = self.archive()
            digest = hashlib.sha256(archive_bytes).hexdigest()

            def download(command, **_):
                Path(command[command.index("--output") + 1]).write_bytes(archive_bytes)

            with patch.object(vulkan_tools, "SHA256", digest), \
                    patch.object(vulkan_tools.subprocess, "run", side_effect=download) as run:
                prepared = vulkan_tools.prepare(cache)
            self.assertEqual(prepared, destination)
            self.assertEqual((prepared / ".sha256").read_text(), digest + "\n")
            self.assertEqual((prepared / "bin/glslc").read_bytes(), b"fixture")
            self.assertEqual(run.call_args.args[0][0], "curl")

    def test_prepare_rejects_download_digest_mismatch(self):
        with tempfile.TemporaryDirectory() as temporary:
            cache = Path(temporary)
            def download(command, **_):
                Path(command[command.index("--output") + 1]).write_bytes(b"wrong")
            with patch.object(vulkan_tools.subprocess, "run", side_effect=download):
                with self.assertRaisesRegex(ValueError, "digest mismatch"):
                    vulkan_tools.prepare(cache)
            self.assertFalse((cache / vulkan_tools.CACHE_NAME).exists())

    @staticmethod
    def archive():
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w") as bundle:
            for name in ("bin/glslc", "include/vulkan/vulkan.h"):
                entry = tarfile.TarInfo(f"{vulkan_tools.VERSION}/x86_64/{name}")
                entry.size = 7
                bundle.addfile(entry, io.BytesIO(b"fixture"))
        return buffer.getvalue()

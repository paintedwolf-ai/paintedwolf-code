"""SDK extraction retains only bounded compiler inputs."""

import io
from pathlib import Path
import tarfile
import tempfile
import unittest

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

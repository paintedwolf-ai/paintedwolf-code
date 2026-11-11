"""SDK extraction retains only bounded compiler inputs."""

import hashlib
import io
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import vulkan_tools


GLSLC_SCRIPT = "#!/bin/sh\nexit 0\n"


class VulkanToolTests(unittest.TestCase):
    def test_extracts_compiler_and_headers_without_loader(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive = root / "sdk.tar"
            archive.write_bytes(self.archive_bytes(("bin/glslc", "include/vulkan/vulkan.h",
                                                    "share/cmake/SPIRV-Headers/SPIRV-HeadersConfig.cmake",
                                                    "lib/libvulkan.so")))
            output = root / "tools"
            vulkan_tools.unpack(archive, output, vulkan_tools.ARCHIVE_ARCH)
            self.assertEqual((output / "bin/glslc").read_text(), GLSLC_SCRIPT)
            self.assertTrue((output / "bin/glslc").stat().st_mode & 0o100)
            self.assertFalse((output / "lib").exists())
            self.assertTrue((output / "share/cmake/SPIRV-Headers/SPIRV-HeadersConfig.cmake").is_file())

    def test_aarch64_unpack_keeps_metadata_but_not_the_archive_compiler(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive = root / "sdk.tar"
            archive.write_bytes(self.archive_bytes(("bin/glslc", "include/vulkan/vulkan.h",
                                                    "share/cmake/SPIRV-Headers/SPIRV-HeadersConfig.cmake",
                                                    "lib/libvulkan.so")))
            output = root / "tools"
            vulkan_tools.unpack(archive, output, vulkan_tools.AARCH64)
            self.assertFalse((output / "bin").exists())
            self.assertFalse((output / "lib").exists())
            self.assertTrue((output / "include/vulkan/vulkan.h").is_file())
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
                    vulkan_tools.unpack(archive, root / "tools", vulkan_tools.ARCHIVE_ARCH)
                self.assertFalse((root / "escape").exists())

    def test_prepare_reuses_verified_tools_and_downloads_atomically(self):
        with tempfile.TemporaryDirectory() as temporary:
            cache = Path(temporary)
            destination = cache / vulkan_tools.CACHE_NAME
            self.write_executable(destination / "bin/glslc")
            (destination / ".sha256").write_text(vulkan_tools.SHA256 + "\n")
            sink = []
            with patch.object(vulkan_tools, "host_arch", return_value=vulkan_tools.ARCHIVE_ARCH), \
                    patch.object(vulkan_tools.subprocess, "run",
                                 side_effect=self.download_dispatch(b"", sink)):
                self.assertEqual(vulkan_tools.prepare(cache), destination)
            self.assertEqual(sink, [])

            destination.rename(cache / "old-tools")
            archive_bytes = self.archive_bytes(("bin/glslc", "include/vulkan/vulkan.h"))
            digest = hashlib.sha256(archive_bytes).hexdigest()
            sink = []
            with patch.object(vulkan_tools, "host_arch", return_value=vulkan_tools.ARCHIVE_ARCH), \
                    patch.object(vulkan_tools, "SHA256", digest), \
                    patch.object(vulkan_tools.subprocess, "run",
                                 side_effect=self.download_dispatch(archive_bytes, sink)):
                prepared = vulkan_tools.prepare(cache)
            self.assertEqual(prepared, destination)
            self.assertEqual((prepared / ".sha256").read_text(), digest + "\n")
            self.assertEqual((prepared / "bin/glslc").read_text(), GLSLC_SCRIPT)
            self.assertEqual(sink[0][0], "curl")

    def test_prepare_rejects_download_digest_mismatch(self):
        with tempfile.TemporaryDirectory() as temporary:
            cache = Path(temporary)
            sink = []
            with patch.object(vulkan_tools, "host_arch", return_value=vulkan_tools.ARCHIVE_ARCH), \
                    patch.object(vulkan_tools.subprocess, "run",
                                 side_effect=self.download_dispatch(b"wrong", sink)):
                with self.assertRaisesRegex(ValueError, "digest mismatch"):
                    vulkan_tools.prepare(cache)
            self.assertFalse((cache / vulkan_tools.CACHE_NAME).exists())

    def test_prepare_provisions_system_glslc_on_aarch64(self):
        with tempfile.TemporaryDirectory() as temporary:
            cache = Path(temporary)
            system = cache / "system-glslc"
            self.write_executable(system)
            archive_bytes = self.archive_bytes(("include/vulkan/vulkan.h",
                                                "share/cmake/Vulkan/VulkanConfig.cmake"))
            digest = hashlib.sha256(archive_bytes).hexdigest()
            sink = []
            with patch.object(vulkan_tools, "host_arch", return_value=vulkan_tools.AARCH64), \
                    patch.object(vulkan_tools, "SHA256", digest), \
                    patch.object(vulkan_tools.shutil, "which", return_value=str(system)), \
                    patch.object(vulkan_tools.subprocess, "run",
                                 side_effect=self.download_dispatch(archive_bytes, sink)):
                prepared = vulkan_tools.prepare(cache)
            self.assertEqual((prepared / "bin/glslc").read_text(), GLSLC_SCRIPT)
            self.assertTrue((prepared / "bin/glslc").stat().st_mode & 0o100)
            self.assertEqual((prepared / ".sha256").read_text(), f"{digest}:aarch64\n")
            self.assertTrue((prepared / "include/vulkan/vulkan.h").is_file())
            self.assertTrue((prepared / "share/cmake/Vulkan/VulkanConfig.cmake").is_file())
            self.assertEqual(sink[0][0], "curl")

    def test_prepare_requires_system_glslc_on_aarch64(self):
        with tempfile.TemporaryDirectory() as temporary:
            cache = Path(temporary)
            archive_bytes = self.archive_bytes(("include/vulkan/vulkan.h",))
            digest = hashlib.sha256(archive_bytes).hexdigest()
            with patch.object(vulkan_tools, "host_arch", return_value=vulkan_tools.AARCH64), \
                    patch.object(vulkan_tools, "SHA256", digest), \
                    patch.object(vulkan_tools.shutil, "which", return_value=None), \
                    patch.object(vulkan_tools.subprocess, "run",
                                 side_effect=self.download_dispatch(archive_bytes, [])):
                with self.assertRaisesRegex(ValueError, "system glslc"):
                    vulkan_tools.prepare(cache)
            self.assertFalse((cache / vulkan_tools.CACHE_NAME).exists())

    def test_prepare_rejects_unexecutable_glslc(self):
        with tempfile.TemporaryDirectory() as temporary:
            cache = Path(temporary)
            system = cache / "system-glslc"
            system.write_text("plain text without shebang")
            system.chmod(0o755)
            archive_bytes = self.archive_bytes(("include/vulkan/vulkan.h",))
            digest = hashlib.sha256(archive_bytes).hexdigest()
            with patch.object(vulkan_tools, "host_arch", return_value=vulkan_tools.AARCH64), \
                    patch.object(vulkan_tools, "SHA256", digest), \
                    patch.object(vulkan_tools.shutil, "which", return_value=str(system)), \
                    patch.object(vulkan_tools.subprocess, "run",
                                 side_effect=self.download_dispatch(archive_bytes, [])):
                with self.assertRaisesRegex(ValueError, "cannot be executed"):
                    vulkan_tools.prepare(cache)
            self.assertFalse((cache / vulkan_tools.CACHE_NAME).exists())

    def test_prepare_reprovisions_when_cached_glslc_is_unusable(self):
        archive_bytes = self.archive_bytes(("bin/glslc", "include/vulkan/vulkan.h"))
        digest = hashlib.sha256(archive_bytes).hexdigest()
        for broken in ("missing", "not-executable"):
            with self.subTest(broken=broken), tempfile.TemporaryDirectory() as temporary:
                cache = Path(temporary)
                destination = cache / vulkan_tools.CACHE_NAME
                self.write_executable(destination / "bin/glslc")
                (destination / ".sha256").write_text(vulkan_tools.SHA256 + "\n")
                if broken == "missing":
                    (destination / "bin/glslc").unlink()
                else:
                    (destination / "bin/glslc").chmod(0o644)
                sink = []
                with patch.object(vulkan_tools, "host_arch", return_value=vulkan_tools.ARCHIVE_ARCH), \
                        patch.object(vulkan_tools, "SHA256", digest), \
                        patch.object(vulkan_tools.subprocess, "run",
                                     side_effect=self.download_dispatch(archive_bytes, sink)):
                    prepared = vulkan_tools.prepare(cache)
                self.assertEqual(len(sink), 1)
                self.assertEqual((prepared / "bin/glslc").read_text(), GLSLC_SCRIPT)
                self.assertTrue((prepared / "bin/glslc").stat().st_mode & 0o100)

    def test_identity_and_cache_reuse_distinguish_architectures(self):
        self.assertEqual(vulkan_tools.identity(vulkan_tools.ARCHIVE_ARCH), vulkan_tools.SHA256)
        self.assertEqual(vulkan_tools.identity(vulkan_tools.AARCH64), vulkan_tools.SHA256 + ":aarch64")
        with tempfile.TemporaryDirectory() as temporary:
            cache = Path(temporary)
            destination = cache / vulkan_tools.CACHE_NAME
            self.write_executable(destination / "bin/glslc")
            (destination / ".sha256").write_text(vulkan_tools.SHA256 + "\n")
            headers = self.archive_bytes(("include/vulkan/vulkan.h",))
            headers_digest = hashlib.sha256(headers).hexdigest()
            sink = []
            with patch.object(vulkan_tools, "host_arch", return_value=vulkan_tools.AARCH64), \
                    patch.object(vulkan_tools, "SHA256", headers_digest), \
                    patch.object(vulkan_tools.shutil, "which", return_value=None), \
                    patch.object(vulkan_tools.subprocess, "run",
                                 side_effect=self.download_dispatch(headers, sink)):
                with self.assertRaisesRegex(ValueError, "system glslc"):
                    vulkan_tools.prepare(cache)
            self.assertEqual(len(sink), 1)
            self.assertEqual((destination / ".sha256").read_text(), vulkan_tools.SHA256 + "\n")

            full = self.archive_bytes(("bin/glslc", "include/vulkan/vulkan.h"))
            full_digest = hashlib.sha256(full).hexdigest()
            (destination / ".sha256").write_text(vulkan_tools.SHA256 + ":aarch64\n")
            sink = []
            with patch.object(vulkan_tools, "host_arch", return_value=vulkan_tools.ARCHIVE_ARCH), \
                    patch.object(vulkan_tools, "SHA256", full_digest), \
                    patch.object(vulkan_tools.subprocess, "run",
                                 side_effect=self.download_dispatch(full, sink)):
                prepared = vulkan_tools.prepare(cache)
            self.assertEqual(len(sink), 1)
            self.assertEqual((prepared / ".sha256").read_text(), full_digest + "\n")
            self.assertEqual((prepared / "bin/glslc").read_text(), GLSLC_SCRIPT)

    @staticmethod
    def write_executable(path):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(GLSLC_SCRIPT)
        path.chmod(0o755)

    @staticmethod
    def archive_bytes(names):
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w") as bundle:
            for name in names:
                payload = GLSLC_SCRIPT.encode() if name == "bin/glslc" else b"fixture"
                entry = tarfile.TarInfo(f"{vulkan_tools.VERSION}/x86_64/{name}")
                entry.size = len(payload)
                bundle.addfile(entry, io.BytesIO(payload))
        return buffer.getvalue()

    @staticmethod
    def download_dispatch(archive_bytes, sink):
        real_run = vulkan_tools.subprocess.run

        def dispatch(command, **kwargs):
            if command[0] == "curl":
                sink.append(command)
                Path(command[command.index("--output") + 1]).write_bytes(archive_bytes)
                return None
            return real_run(command, **kwargs)

        return dispatch

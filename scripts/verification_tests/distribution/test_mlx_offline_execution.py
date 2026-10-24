"""CMake consumes checksummed repository sources before FetchContent can fetch."""

import hashlib
import io
import json
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]
TOOLCHAIN = ROOT / "scripts/toolchain/mlx-offline.cmake"


class MLXOfflineSource(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        toolchain = self.root / "scripts/toolchain/mlx-offline.cmake"
        toolchain.parent.mkdir(parents=True)
        shutil.copyfile(TOOLCHAIN, toolchain)
        self.vendor = self.root / "third_party/mlx"
        self.vendor.mkdir(parents=True)
        self.crate = self.root / "mlx-sys"
        self.source = self.crate / "src/mlx-c"
        (self.source / "mlx/c").mkdir(parents=True)
        (self.source / "mlx/c/array.h").touch()
        (self.crate / "Cargo.toml").write_text('[package]\nname = "mlx-sys"\nversion = "0.6.0"\n')
        (self.source / "CMakeLists.txt").write_text('FetchContent_Declare(mlx GIT_TAG v0.32.2)\n')
        archive = self.vendor / "source.tar.gz"
        with tarfile.open(archive, "w:gz") as packed:
            entry = tarfile.TarInfo("mlx-source/CMakeLists.txt")
            body = b"# Pinned source fixture\n"
            entry.size = len(body)
            packed.addfile(entry, io.BytesIO(body))
        self.pin = {"mlx_sys_version": "0.6.0", "sources": [
            {"name": "mlx", "archive": archive.name, "sha256": hashlib.sha256(archive.read_bytes()).hexdigest(),
             "directory": "mlx-source", "tag": "v0.32.2"}]}
        self.script = self.root / "check.cmake"
        self.script.write_text(f'set(CMAKE_CURRENT_SOURCE_DIR "{self.source}")\n'
                               f'set(CMAKE_BINARY_DIR "{self.root / "build"}")\n'
                               'set(CMAKE_HOST_APPLE "${PW_TEST_APPLE}")\n'
                               f'include("{toolchain}")\n'
                               'if(CMAKE_HOST_APPLE)\n'
                               '  if(NOT FETCHCONTENT_FULLY_DISCONNECTED OR NOT EXISTS "${FETCHCONTENT_SOURCE_DIR_MLX}/CMakeLists.txt")\n'
                               '    message(FATAL_ERROR "offline source was not selected")\n'
                               '  endif()\n'
                               'endif()\n')

    def run_cmake(self, apple=True):
        (self.vendor / "pin.json").write_text(json.dumps(self.pin))
        return subprocess.run(["cmake", f"-DPW_TEST_APPLE={'ON' if apple else 'OFF'}",
                               f"-DCMAKE_CURRENT_SOURCE_DIR={self.source}",
                               f"-DCMAKE_BINARY_DIR={self.root / 'build'}", "-P", str(self.script)],
                              text=True, capture_output=True, timeout=30)

    def test_verified_source_is_selected_with_fetching_disconnected(self):
        result = self.run_cmake()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_changed_archive_is_refused_before_any_fetch(self):
        (self.vendor / "source.tar.gz").write_bytes(b"wrong source")
        result = self.run_cmake()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("MLX_SOURCE_DIGEST_MISMATCH", result.stderr)

    def test_missing_archive_is_refused_before_any_fetch(self):
        (self.vendor / "source.tar.gz").unlink()
        result = self.run_cmake()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("MLX_SOURCE_MISSING", result.stderr)

    def test_crate_upgrade_requires_reviewing_pin(self):
        (self.crate / "Cargo.toml").write_text('name = "mlx-sys"\nversion = "0.7.0"\n')
        result = self.run_cmake()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("MLX_SOURCE_PIN_MISMATCH", result.stderr)

    def test_upstream_cpp_tag_change_requires_reviewing_pin(self):
        self.pin["sources"][0]["tag"] = "v0.33.0"
        result = self.run_cmake()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("MLX_SOURCE_PIN_MISMATCH", result.stderr)

    def test_non_apple_projects_need_no_source_archive(self):
        (self.vendor / "source.tar.gz").unlink()
        result = self.run_cmake(apple=False)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_persistent_source_and_toolchain_use_explicit_current_repository_inputs(self):
        self.assertEqual(self.run_cmake().returncode, 0)
        original = self.root / 'scripts/toolchain/mlx-offline.cmake'
        persistent = self.root / 'persistent'
        persistent.mkdir()
        toolchain = persistent / 'mlx-offline.cmake'
        shutil.copyfile(original, toolchain)
        original.unlink()
        source = persistent / 'mlx-c'
        shutil.move(self.source, source)
        script = self.script.read_text().replace(str(original), str(toolchain)).replace(str(self.source), str(source))
        self.script.write_text(f'set(PW_MLX_VENDOR_ROOT "{self.vendor}")\n'
                               f'set(PW_MLX_SYS_MANIFEST "{self.crate / "Cargo.toml"}")\n' + script)
        result = self.run_cmake()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.pin['mlx_sys_version'] = '0.7.0'
        rejected = self.run_cmake()
        self.assertNotEqual(rejected.returncode, 0)
        self.assertIn('MLX_SOURCE_PIN_MISMATCH', rejected.stderr)

    def test_ordinary_cargo_builds_select_same_toolchain(self):
        config = (ROOT / ".cargo/config.toml").read_text()
        self.assertIn('[env]\n', config)
        manifest = (ROOT / 'lycaon/internal/decide/native/Cargo.toml').read_text()
        self.assertIn('mlx-sys = { path = "vendor/mlx-sys" }', manifest)
        self.assertIn('CMAKE_TOOLCHAIN_FILE = { value = "scripts/toolchain/mlx-offline.cmake", '
                      'relative = true, force = true }', config)
        pin = json.loads((ROOT / "third_party/mlx/pin.json").read_text())
        for source in pin["sources"]:
            archive = ROOT / "third_party/mlx" / source["archive"]
            self.assertEqual(hashlib.sha256(archive.read_bytes()).hexdigest(), source["sha256"])

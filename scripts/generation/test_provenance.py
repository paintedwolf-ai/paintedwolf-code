"""Measurements must use the executable built from their recorded sources."""

from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import provenance


class ProvenanceTests(unittest.TestCase):
    def test_reference_source_identity_tracks_only_backend_inputs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for directory, files in (
                    ("llama-reference", ("CMakeLists.txt", "main.cpp", "cache.patch", "ignored.txt")),
                    ("swift-reference", ("main.swift", "Package.resolved", "ignored.txt"))):
                for name in files:
                    path = root / directory / name
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_text(name)
            (root / "sources.json").write_text("{}")
            with patch.object(provenance, "ROOT", root):
                llama = provenance.source_identity("reference-llama")
                swift = provenance.source_identity("reference-swift")
                self.assertNotEqual(llama, swift)
                (root / "llama-reference/ignored.txt").write_text("changed")
                self.assertEqual(llama, provenance.source_identity("reference-llama"))
                (root / "llama-reference/cache.patch").write_text("changed")
                self.assertNotEqual(llama, provenance.source_identity("reference-llama"))

    def test_rust_identity_includes_patched_bindings_and_build_recipe(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            scripts = root / "scripts/generation"
            inputs = ["lycaon/internal/generation/native/src/mlx_engine/mod.rs",
                      "lycaon/internal/generation/native/Cargo.toml",
                      "lycaon/internal/generation/native/Cargo.lock",
                      "lycaon/internal/decide/native/vendor/mlx-sys/paintedwolf_inputs.rs",
                      "third_party/mlx/pin.json", "third_party/mlx/e4b-d512.patch", "scripts/toolchain/mlx-offline.cmake",
                      "scripts/generation/runtime.py", "scripts/generation/provenance.py",
                      "scripts/generation/sources.json", "scripts/generation/sources.py",
                      "scripts/generation/llama-reference/cache-cursor.patch",
                      "scripts/generation/llama-reference/shared-kv-prefill.patch",
                      "scripts/generation/llama-reference/CMakeLists.txt",
                      "lycaon/internal/generation/native/build.rs",
                      "lycaon/internal/generation/templates/gemma4-code-v1.jinja",
                      "lycaon/internal/generation/native/vulkan/inventory.cpp",
                      "lycaon/internal/decide/native/vendor/mlx-sys/src/mlx-c/mlx/c/device.cpp"]
            for name in inputs:
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("original")
            with patch.object(provenance, "ROOT", scripts):
                for backend in ("reference-rust", "native"):
                    identity = provenance.source_identity(backend)
                    for name in inputs:
                        path = root / name
                        path.write_text("changed")
                        self.assertNotEqual(identity, provenance.source_identity(backend), name)
                        path.write_text("original")

    def test_changed_sources_or_executable_require_a_new_build(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "llama-reference").mkdir()
            source = root / "llama-reference/main.cpp"
            source.write_text("original source")
            header = root / "llama-reference/memory.h"
            header.write_text("original header")
            (root / "sources.json").write_text("{}")
            binary = root / "generation-reference"
            binary.write_bytes(b"original executable")
            cache = root / "CMakeCache.txt"
            cache.write_text("GGML_AVX:BOOL=ON")
            with patch.object(provenance, "ROOT", root), patch.object(provenance, "swift_identity", return_value={"version": "fixture"}):
                provenance.record_build("reference-llama", root, binary)
                receipt = provenance.verified_build("reference-llama", root, binary)
                self.assertEqual(receipt["binary_sha256"], provenance.file_digest(binary))
                for path in (source, header, binary, cache):
                    original = path.read_bytes()
                    path.write_bytes(b"changed")
                    with self.assertRaisesRegex(ValueError, "rebuild"):
                        provenance.verified_build("reference-llama", root, binary)
                    path.write_bytes(original)

    def test_swift_shader_changes_require_a_new_build(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "swift-reference").mkdir()
            (root / "swift-reference/Package.swift").write_text("package")
            (root / "sources.json").write_text("{}")
            (root / "metal").mkdir()
            shader = root / "metal/mlx.metallib"
            shader.write_bytes(b"shader")
            binary = root / "runner"
            binary.write_bytes(b"executable")
            with patch.object(provenance, "ROOT", root), patch.object(provenance, "swift_identity", return_value={"version": "fixture"}):
                provenance.record_build("reference-swift", root, binary)
                provenance.verified_build("reference-swift", root, binary)
                with patch.object(provenance, "swift_identity", return_value={"version": "different"}):
                    with self.assertRaisesRegex(ValueError, "rebuild"):
                        provenance.verified_build("reference-swift", root, binary)
                shader.write_bytes(b"changed shader")
                with self.assertRaisesRegex(ValueError, "rebuild"):
                    provenance.verified_build("reference-swift", root, binary)

    def test_native_shared_library_and_separate_runtime_receipts(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binary = root / "nativegen"
            binary.write_bytes(b"native executable")
            reference = root / "generation-reference"
            reference.write_bytes(b"reference executable")
            adapter = root / "libnativegen_vulkan.so"
            adapter.write_bytes(b"adapter")
            with patch.object(provenance.sys, "platform", "linux"), \
                    patch.object(provenance, "source_identity", return_value="source"), \
                    patch.object(provenance.subprocess, "check_output", return_value="compiler"):
                provenance.record_build("native", root, binary)
                provenance.record_build("reference-llama", root, reference)
                provenance.verified_build("native", root, binary)
                provenance.verified_build("reference-llama", root, reference)
                adapter.write_bytes(b"changed adapter")
                with self.assertRaisesRegex(ValueError, "rebuild"):
                    provenance.verified_build("native", root, binary)

    def test_darwin_native_receipt_binds_compiler_sdk_and_shader(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binary = root / "nativegen"
            binary.write_bytes(b"native executable")
            (root / "metal").mkdir()
            (root / "metal/mlx.metallib").write_bytes(b"shader")
            with patch.object(provenance.sys, "platform", "darwin"), \
                    patch.object(provenance, "source_identity", return_value="source"), \
                    patch.object(provenance.subprocess, "check_output",
                                 side_effect=["rustc fixture", "cargo fixture", "14.0", "/sdk"]):
                identity = provenance.build_identity("native", root, binary)
            self.assertEqual(identity["toolchain"], {"rustc": "rustc fixture", "cargo": "cargo fixture"})
            self.assertEqual(identity["sdk"], {"version": "14.0", "path": "/sdk"})
            self.assertEqual(identity["metallib_sha256"], provenance.file_digest(root / "metal/mlx.metallib"))

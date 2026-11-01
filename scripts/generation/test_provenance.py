"""Measurements must use the executable built from their recorded sources."""

from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import provenance


class ProvenanceTests(unittest.TestCase):
    def test_rust_identity_includes_patched_bindings_and_build_recipe(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            scripts = root / "scripts/generation"
            inputs = ["lycaon/internal/generation/native/src/mlx_engine/mod.rs",
                      "lycaon/internal/generation/native/Cargo.toml",
                      "lycaon/internal/generation/native/Cargo.lock",
                      "lycaon/internal/decide/native/vendor/mlx-sys/paintedwolf_inputs.rs",
                      "third_party/mlx/pin.json", "scripts/toolchain/mlx-offline.cmake",
                      "scripts/generation/runtime.py", "scripts/generation/provenance.py",
                      "scripts/generation/sources.json", "scripts/generation/sources.py",
                      "scripts/generation/llama-reference/cache-cursor.patch",
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
            with patch.object(provenance, "ROOT", root), patch.object(provenance, "swift_identity", return_value={"version": "fixture"}):
                provenance.record_build("reference-llama", root, binary)
                receipt = provenance.verified_build("reference-llama", root, binary)
                self.assertEqual(receipt["binary_sha256"], provenance.file_digest(binary))
                for path in (source, header, binary):
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

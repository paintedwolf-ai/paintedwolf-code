"""Measurements must use the executable built from their recorded sources."""

from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import provenance


class ProvenanceTests(unittest.TestCase):
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

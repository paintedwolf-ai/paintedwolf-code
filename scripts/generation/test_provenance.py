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
            (root / "sources.json").write_text("{}")
            binary = root / "generation-reference"
            binary.write_bytes(b"original executable")
            with patch.object(provenance, "ROOT", root):
                provenance.record_build("reference-llama", root, binary)
                receipt = provenance.verified_build("reference-llama", root, binary)
                self.assertEqual(receipt["binary_sha256"], provenance.file_digest(binary))
                for path in (source, binary):
                    original = path.read_bytes()
                    path.write_bytes(b"changed")
                    with self.assertRaisesRegex(ValueError, "rebuild"):
                        provenance.verified_build("reference-llama", root, binary)
                    path.write_bytes(original)

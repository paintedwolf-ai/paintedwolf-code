"""Partial diagnostics preserve bytes and refuse to grant candidate authority."""
import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release.candidate_diagnostics import retain


class PartialCandidateDiagnosticsTests(unittest.TestCase):
    def test_retains_selected_bytes_with_explicit_unqualified_scope(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            source=root/"source"
            package=source/"lycaon-den/src-tauri/target/aarch64-unknown-linux-gnu/release/bundle/deb/painted-wolf-code.deb"
            package.parent.mkdir(parents=True)
            package.write_bytes(b"actual partial bytes")
            output=root/"diagnostics"
            with patch("linux_release.candidate_diagnostics.subprocess.check_output",side_effect=["a"*40+"\n","b"*40+"\n"]):
                report=retain(source,output,"c"*40)
            self.assertEqual((output/"0-painted-wolf-code.deb").read_bytes(),package.read_bytes())
            self.assertEqual(report["packages"][0]["sha256"],hashlib.sha256(package.read_bytes()).hexdigest())
            for field in ("complete_candidate","installed_qualified","publication_authority"):
                self.assertIs(report[field],False)
            self.assertEqual(report["source_commit"],"a"*40)
            self.assertEqual(report["controller_commit"],"c"*40)

    def test_symlink_payload_and_reused_output_refuse(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            source=root/"source"
            package=source/"lycaon-den/src-tauri/target/arm/release/bundle/deb/painted-wolf-code.deb"
            package.parent.mkdir(parents=True)
            outside=root/"actual.deb"
            outside.write_bytes(b"outside")
            package.symlink_to(outside)
            with patch("linux_release.candidate_diagnostics.subprocess.check_output",side_effect=["a"*40,"b"*40]):
                with self.assertRaises((OSError,ValueError)):
                    retain(source,root/"refused","c"*40)
            self.assertFalse((root/"refused").exists())
            with self.assertRaisesRegex(ValueError,"fresh"):
                retain(source,source/"output","c"*40)

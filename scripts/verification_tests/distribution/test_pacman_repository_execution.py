"""Pacman generations refuse ambiguous package identities and selected-input drift."""

import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release.pacman_repository import assemble, package_info


class PacmanRepositoryTests(unittest.TestCase):
    def test_pkginfo_uses_exact_structured_identity_and_rejects_duplicates(self):
        valid = "# generated\npkgname = painted-wolf-code\npkgver = 1.0.2pre.rc6-1\narch = x86_64\nlicense = MIT\nlicense = Apache-2.0\n"
        with patch("linux_release.pacman_repository.subprocess.check_output", return_value=valid):
            self.assertEqual(package_info(Path("package")), {"pkgname": "painted-wolf-code", "pkgver": "1.0.2pre.rc6-1", "arch": "x86_64"})
        for raw in [valid + "arch = aarch64\n", valid.replace("arch = x86_64\n", ""), valid + "unframed value\n"]:
            with self.subTest(raw=raw), patch("linux_release.pacman_repository.subprocess.check_output", return_value=raw), self.assertRaises(ValueError):
                package_info(Path("package"))

    def test_hash_and_architecture_drift_refuse_generation_before_creation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            package = root / "painted-wolf-code.pkg.tar.zst"
            package.write_bytes(b"selected fixture")
            manifest = {"format_version": 1, "source_commit": "a" * 40, "version": "1.0.2-rc6", "architecture": "x86_64",
                        "packages": [{"file": package.name, "native_version": "1.0.2pre.rc6-1", "sha256": hashlib.sha256(package.read_bytes()).hexdigest()}]}
            destination = root / "generation"
            wrong = {"pkgname": "painted-wolf-code", "pkgver": "1.0.2pre.rc6-1", "arch": "aarch64"}
            with patch("linux_release.pacman_repository.package_info", return_value=wrong), self.assertRaises(ValueError):
                assemble(manifest, root, destination, "stable", root / "keys", root / "private")
            self.assertFalse(destination.exists())
            package.write_bytes(b"changed")
            with patch("linux_release.pacman_repository.package_info") as parser, self.assertRaises(ValueError):
                assemble(manifest, root, destination, "stable", root / "keys", root / "private")
            parser.assert_not_called()
            self.assertFalse(destination.exists())

    def test_unknown_channel_and_unsupported_architecture_are_not_published(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for channel, manifest in [("nightly", {}), ("stable", {"format_version": 1, "source_commit": "a" * 40, "architecture": "any"})]:
                with self.subTest(channel=channel), self.assertRaises(ValueError):
                    assemble(manifest, root, root / "generation", channel, root / "keys", root / "private")
                self.assertFalse((root / "generation").exists())

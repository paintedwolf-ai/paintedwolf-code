"""Native repository inputs must retain selected hashes and package identities."""

import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release.native_repositories import assemble, checked_input


class NativeRepositoryInputsTests(unittest.TestCase):
    def fixture(self, directory):
        path = Path(directory) / "painted-wolf-code.deb"
        path.write_bytes(b"selected package fixture")
        return path, {"file": path.name, "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
                      "architecture": "amd64", "native_version": "1.0.2~rc6"}

    def test_matching_hash_still_requires_native_name_version_and_architecture(self):
        with tempfile.TemporaryDirectory() as directory:
            path, row = self.fixture(directory)
            with patch("linux_release.native_repositories.subprocess.check_output", return_value="painted-wolf-code\t1.0.2~rc6\tamd64"):
                self.assertEqual(checked_input(row, path.parent, "deb", "painted-wolf-code"), path)
            for actual in ["other\t1.0.2~rc6\tamd64", "painted-wolf-code\t1.0.2\tamd64",
                           "painted-wolf-code\t1.0.2~rc6\tarm64"]:
                with self.subTest(actual=actual), patch("linux_release.native_repositories.subprocess.check_output", return_value=actual), self.assertRaises(ValueError):
                    checked_input(row, path.parent, "deb", "painted-wolf-code")

    def test_tampered_escaping_and_symlink_inputs_never_reach_the_package_parser(self):
        with tempfile.TemporaryDirectory() as directory:
            path, row = self.fixture(directory)
            for mutation in [dict(row, sha256="0" * 64), dict(row, file="../escape.deb")]:
                with self.subTest(mutation=mutation), patch("linux_release.native_repositories.subprocess.check_output") as parser, self.assertRaises(ValueError):
                    checked_input(mutation, path.parent, "deb", "painted-wolf-code")
                parser.assert_not_called()
            real = path.with_suffix(".real")
            path.rename(real)
            path.symlink_to(real)
            with self.assertRaises(ValueError):
                checked_input(row, path.parent, "deb", "painted-wolf-code")

    def test_invalid_generation_cannot_allocate_or_publish_a_destination(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            destination = root / "generation"
            for family, channel, manifest in [("deb", "nightly", {}), ("rpm", "stable", {}), ("pacman", "stable", {})]:
                with self.subTest(family=family, channel=channel), self.assertRaises(ValueError):
                    assemble(family, manifest, root, destination, channel, private=root / "key")
                self.assertFalse(destination.exists())

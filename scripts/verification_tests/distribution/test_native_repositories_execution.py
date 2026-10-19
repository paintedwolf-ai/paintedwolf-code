"""Native repository inputs must retain selected hashes and package identities."""

import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release.native_repositories import assemble, checked_input
from linux_release.repository_validation import admit, check_bytes, selected_object


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

    def test_generation_inventory_refuses_duplicates_unlisted_bytes_and_wrong_trust(self):
        with tempfile.TemporaryDirectory() as directory:
            path, row = self.fixture(directory)
            item = {"path": path.name, "sha256": row["sha256"], "size": path.stat().st_size}
            signer = {"generation": 1, "fingerprint": "A" * 40}
            receipt = {"format_version": 1, "family": "deb", "channel": "preview", "rehearsal": True,
                       "source_commit": "f" * 40, "version": "1.0.2-rc6", "signing_generation": 1,
                       "key_fingerprint": signer["fingerprint"], "objects": [item], "packages": [row]}

            def inventory(value):
                return dict(value, inventory_sha256=hashlib.sha256(
                    json.dumps(value["objects"], sort_keys=True, separators=(",", ":")).encode()).hexdigest())

            receipt = inventory(receipt)
            changes = [(dict(receipt, objects=[item, item]), "repeats an object"),
                       (dict(receipt, objects=[]), "inventory is empty"),
                       (dict(receipt, rehearsal=False), "cross-trust"),
                       (dict(receipt, objects=[dict(item, sha256="0" * 64)]), "selected final bytes")]
            with patch("linux_release.repository_validation.selection", return_value=(signer, Path("public"))):
                for changed, reason in changes:
                    with self.subTest(reason=reason), patch("linux_release.repository_validation.apt") as crypto:
                        with self.assertRaisesRegex(ValueError, reason):
                            admit(inventory(changed), path.parent, path.parent / "catalog", True)
                        crypto.assert_not_called()
                (path.parent / "unlisted").write_text("unexpected")
                with self.assertRaisesRegex(ValueError, "inventory is incomplete"):
                    admit(receipt, path.parent, path.parent / "catalog", True)

    def test_object_verification_refuses_escape_symlinks_and_size_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            path, row = self.fixture(directory)
            for name in ["../escape", str(path), "nested/../" + path.name]:
                with self.subTest(name=name), self.assertRaises(ValueError):
                    selected_object(path.parent, name)
            for size in [True, -1, path.stat().st_size - 1, path.stat().st_size + 1]:
                with self.subTest(size=size), self.assertRaises(ValueError):
                    check_bytes(path, row["sha256"], size)

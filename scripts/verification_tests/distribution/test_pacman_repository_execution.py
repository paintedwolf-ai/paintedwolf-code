"""Pacman assembly and admission read native identities and refuse drifted databases, packages or trust."""

import hashlib
import io
import json
from pathlib import Path
import shutil
import tarfile
import tempfile
import unittest

from scripts.linux_release.pacman_repository import assemble, package_info
from scripts.linux_release.pacman_validation import admit, fields
from scripts.verification_tests.distribution.pacman_repository_fixtures import (
    PACKAGE, TESTDATA, generation, inputs, manifest, package_name, trust)

IDENTITY = f"pkgname = {PACKAGE}\npkgver = 0.0.1-1\narch = x86_64\n"
DATABASE = "painted-wolf-code-preview"


def archive(path: Path, pkginfo: str):
    with tarfile.open(path, "w") as stream:
        member = tarfile.TarInfo(".PKGINFO")
        member.size = len(pkginfo.encode())
        stream.addfile(member, io.BytesIO(pkginfo.encode()))


class PacmanRepositoryTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.inputs = inputs(self.work)
        self.catalog = trust(self.work / "trust")
        self.destination = self.work / "generation"

    def assemble(self, selected, channel="preview", package=PACKAGE):
        with self.assertRaises(ValueError) as refused:
            assemble(selected, self.inputs, self.destination, channel, self.catalog, self.work / "unused-key",
                     True, package)
        self.assertFalse(self.destination.exists())
        return str(refused.exception)

    def test_native_identity_comes_from_the_package_metadata(self):
        self.assertEqual(package_info(self.inputs / package_name("0.0.1")),
                         {"pkgname": PACKAGE, "pkgver": "0.0.1-1", "arch": "x86_64"})
        self.assertEqual(package_info(self.inputs / package_name("0.0.1", "aarch64"))["arch"], "aarch64")
        for raw, reason in [("# generated\n" + IDENTITY + "license = MIT\nlicense = Apache-2.0\n", None),
                            (IDENTITY + "arch = aarch64\n", "ambiguous"),
                            (IDENTITY.replace("arch = x86_64\n", ""), "missing identity"),
                            (IDENTITY + "unframed value\n", "framing"),
                            (IDENTITY.replace("x86_64", ""), "ambiguous")]:
            with self.subTest(raw=raw):
                path = self.work / "crafted.pkg.tar.zst"
                archive(path, raw)
                if reason is None:
                    self.assertEqual(package_info(path)["pkgver"], "0.0.1-1")
                else:
                    with self.assertRaisesRegex(ValueError, reason):
                        package_info(path)

    def test_selected_receipt_must_match_the_native_package_before_output(self):
        for selected, package in [(manifest(self.inputs, native_version="0.0.2-1"), PACKAGE),
                                  ({**manifest(self.inputs), "architecture": "aarch64"}, PACKAGE),
                                  (manifest(self.inputs), "painted-wolf-code")]:
            with self.subTest(selected=selected, package=package):
                self.assertIn("identity differs from its selected receipt", self.assemble(selected, package=package))

    def test_tampered_linked_or_misnamed_inputs_refuse_before_output(self):
        for changes, reason in [({"sha256": "0" * 64}, "selected immutable input"),
                                ({"file": "../" + package_name("0.0.1")}, "sibling native package"),
                                ({"file": "fixture.tar.gz"}, "sibling native package")]:
            with self.subTest(reason=reason):
                self.assertIn(reason, self.assemble(manifest(self.inputs, **changes)))
        selected = manifest(self.inputs)
        package = self.inputs / package_name("0.0.1")
        shutil.move(package, self.work / "linked.pkg.tar.zst")
        package.symlink_to(self.work / "linked.pkg.tar.zst")
        self.assertIn("selected immutable input", self.assemble(selected))

    def test_generation_identity_channel_and_architecture_are_explicit(self):
        selected = manifest(self.inputs)
        for changed, channel, reason in [(selected, "nightly", "unsupported pacman generation"),
                                         ({**selected, "format_version": 2}, "preview", "unsupported pacman generation"),
                                         ({**selected, "source_commit": "short"}, "preview", "exact source"),
                                         ({**selected, "architecture": "any"}, "preview", "exact source"),
                                         ({**selected, "version": "../0.0.1"}, "preview", "product version"),
                                         ({**selected, "packages": selected["packages"] * 2}, "preview", "distinct"),
                                         ({**selected, "packages": []}, "preview", "distinct")]:
            with self.subTest(reason=reason, channel=channel):
                self.assertIn(reason, self.assemble(changed, channel))


def rebind(receipt: dict, directory: Path) -> dict:
    """An attacker who also controls the receipt: inventory rows describe the changed files."""
    objects = [{"path": path.name, "sha256": hashlib.sha256(path.read_bytes()).hexdigest(), "size": path.stat().st_size}
               for path in sorted(directory.iterdir()) if path.name != "generation.json"]
    hashes = {row["path"]: row["sha256"] for row in objects}
    packages = [{**row, "sha256": hashes[row["file"]]} for row in receipt["packages"]]
    return {**receipt, "objects": objects, "packages": packages, "inventory_sha256": hashlib.sha256(
        json.dumps(objects, sort_keys=True, separators=(",", ":")).encode()).hexdigest()}


class PacmanValidationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.catalog = trust(self.work / "trust")
        self.receipt, self.directory = generation("pacman-0.0.1-x86_64", self.work)
        self.package = self.directory / self.receipt["packages"][0]["file"]

    def refuses(self, reason, receipt=None, catalog=None, rehearsal=True):
        """Without an explicit receipt, the receipt is rebound to the changed files."""
        receipt = rebind(self.receipt, self.directory) if receipt is None else receipt
        with self.assertRaisesRegex(ValueError, reason):
            admit(receipt, self.directory, catalog or self.catalog, rehearsal)

    def replace(self, name, source="pacman-0.0.2-x86_64"):
        shutil.copyfile(TESTDATA / source / name, self.directory / name)

    def test_committed_generation_admits_only_under_its_selected_rehearsal_trust_and_channel(self):
        admit(self.receipt, self.directory, self.catalog, True)
        self.refuses("cross-trust", self.receipt, rehearsal=False)
        self.refuses("trust mode", {**self.receipt, "rehearsal": False}, rehearsal=False)
        self.refuses("selected signing identity", self.receipt, trust(self.work / "other", "other-keys.json"))
        self.refuses("selected release channel", {**self.receipt, "channel": "stable"})
        self.refuses("cross-trust", {**self.receipt, "architecture": "any"})

    def test_signatures_by_another_key_refuse_when_receipt_and_catalog_name_that_key(self):
        other = trust(self.work / "other", "other-keys.json")
        fingerprint = json.loads(other.read_text())["generations"][0]["fingerprint"]
        self.refuses("gpgv failed", {**self.receipt, "key_fingerprint": fingerprint}, other)

    def test_inventory_refuses_extra_missing_and_changed_objects(self):
        (self.directory / "unlisted").write_text("unexpected")
        self.refuses("differs from its selected objects", self.receipt)
        (self.directory / "unlisted").unlink()
        original = self.package.read_bytes()
        self.package.write_bytes(original + b"changed")
        self.refuses("selected final bytes", self.receipt)
        self.package.unlink()
        self.refuses("differs from its selected objects", self.receipt)
        self.package.write_bytes(original)
        changed = {**self.receipt, "packages": [{**self.receipt["packages"][0], "architecture": "aarch64"}]}
        self.refuses("final package identity", changed)

    def test_database_signatures_and_aliases_bind_the_selected_archive(self):
        self.replace(DATABASE + ".db.tar.gz")
        self.refuses("gpgv failed")
        self.receipt, self.directory = generation("pacman-0.0.1-x86_64", self.work / "alias")
        (self.directory / (DATABASE + ".db")).write_bytes((TESTDATA / "pacman-0.0.2-x86_64" / (DATABASE + ".db")).read_bytes())
        self.refuses("alias differs from its signed archive")

    def test_validly_signed_database_for_another_generation_references_unselected_packages(self):
        for suffix in [".db.tar.gz", ".db.tar.gz.sig", ".db", ".db.sig"]:
            self.replace(DATABASE + suffix)
        self.refuses("unselected or duplicate package")

    def test_database_binds_package_checksums_and_embedded_signatures(self):
        self.package.write_bytes(self.package.read_bytes() + b"changed")
        self.refuses("database checksum differs")
        self.receipt, self.directory = generation("pacman-0.0.1-x86_64", self.work / "signature")
        signature = self.directory / (self.receipt["packages"][0]["file"] + ".sig")
        signature.write_bytes((TESTDATA / "pacman-0.0.1-aarch64" / signature.name.replace("x86_64", "aarch64")).read_bytes())
        self.refuses("embedded signature differs")

    def test_database_descriptions_have_unambiguous_fields(self):
        self.assertEqual(fields(b"%NAME%\nfixture\n\n%VERSION%\n0.0.1-1\n"),
                         {"NAME": ["fixture"], "VERSION": ["0.0.1-1"]})
        for raw in [b"%NAME%\nfixture\n\n%NAME%\nother\n", b"NAME\nfixture\n", b"%NAME%\n"]:
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                fields(raw)


if __name__ == "__main__":
    unittest.main()

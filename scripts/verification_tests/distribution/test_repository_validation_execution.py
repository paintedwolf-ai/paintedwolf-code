"""Signed apt/dnf admission refuses committed generations whose bytes, indexes or trust differ."""

import hashlib
import json
from pathlib import Path
import shutil
import tempfile
import unittest

from scripts.linux_release.repository_validation import admit, check_bytes, selected_object
from scripts.verification_tests.distribution.native_repository_fixtures import (
    TESTDATA, generation, native_tools, trust)


def rebind(receipt: dict, directory: Path) -> dict:
    """An attacker who also controls the receipt: inventory rows describe the changed files."""
    objects = [{"path": path.relative_to(directory).as_posix(), "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
                "size": path.stat().st_size}
               for path in sorted(directory.rglob("*")) if path.is_file() and path.name != "generation.json"]
    hashes = {row["path"]: row["sha256"] for row in objects}
    packages = [{**row, "sha256": hashes[row["file"]]} for row in receipt["packages"]]
    return {**receipt, "objects": objects, "packages": packages, "inventory_sha256": hashlib.sha256(
        json.dumps(objects, sort_keys=True, separators=(",", ":")).encode()).hexdigest()}


class RepositoryValidationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.catalog = trust(self.work / "trust")

    def refuses(self, receipt, directory, reason, catalog=None, rehearsal=True):
        with self.assertRaisesRegex(ValueError, reason):
            admit(receipt, directory, catalog or self.catalog, rehearsal)

    def test_committed_signed_generations_admit_only_under_their_selected_rehearsal_trust(self):
        receipt, directory = generation("deb-0.0.1", self.work)
        admit(receipt, directory, self.catalog, True)
        self.refuses(receipt, directory, "cross-trust", rehearsal=False)
        self.refuses({**receipt, "rehearsal": False}, directory, "cross-trust")
        self.refuses(receipt, directory, "selected signing identity", trust(self.work / "other", "other-keys.json"))

    def test_signatures_by_another_key_refuse_even_when_receipt_and_catalog_name_that_key(self):
        receipt, directory = generation("deb-0.0.1", self.work)
        other = trust(self.work / "other", "other-keys.json")
        fingerprint = json.loads(other.read_text())["generations"][0]["fingerprint"]
        self.refuses({**receipt, "key_fingerprint": fingerprint}, directory, "gpgv failed", other)
        swapped = json.loads(self.catalog.read_text())
        swapped["generations"][0]["public_key"] = "other-public.asc"
        shutil.copyfile(TESTDATA / "other-public.asc", self.catalog.parent / "other-public.asc")
        self.catalog.write_text(json.dumps(swapped))
        self.refuses(receipt, directory, "public key differs from the catalog")

    def test_inventory_refuses_extra_missing_changed_and_linked_objects_before_signature_checks(self):
        receipt, directory = generation("deb-0.0.1", self.work)
        package = directory / receipt["packages"][0]["file"]
        (directory / "unlisted").write_text("unexpected")
        self.refuses(receipt, directory, "unexpected files")
        (directory / "unlisted").unlink()
        original = package.read_bytes()
        package.write_bytes(original + b"changed")
        self.refuses(receipt, directory, "selected size")
        package.unlink()
        self.refuses(receipt, directory, "unexpected files")
        retained = self.work / "retained.deb"
        retained.write_bytes(original)
        package.symlink_to(retained)
        self.refuses(receipt, directory, "escapes its selected generation")
        row = receipt["objects"][0]
        for changed, reason in [({"objects": [row, row]}, "repeats an object"), ({"objects": []}, "inventory is empty"),
                                ({"inventory_sha256": "0" * 64}, "selected identity")]:
            with self.subTest(reason=reason):
                value = {**receipt, **changed}
                if "objects" in changed:
                    value = {**value, "inventory_sha256": hashlib.sha256(json.dumps(
                        value["objects"], sort_keys=True, separators=(",", ":")).encode()).hexdigest()}
                self.refuses(value, directory, reason)

    def test_signed_release_binds_package_indexes_by_hash_aliases_and_pool_bytes(self):
        index = "dists/preview/main/binary-amd64/Packages"
        for name, reason in [(index, "selected size"), ("pool/", "selected size"),
                             ("by-hash/SHA512", "SHA512 by-hash alias")]:
            with self.subTest(name=name):
                receipt, directory = generation("deb-0.0.1", self.work / name.replace("/", "-"))
                path = next(path for path in sorted(directory.rglob("*"))
                            if path.is_file() and name in path.relative_to(directory).as_posix())
                path.write_bytes(path.read_bytes() + b"\nchanged\n")
                self.refuses(rebind(receipt, directory), directory, reason)

    def test_release_and_inrelease_must_be_the_same_signed_content(self):
        receipt, directory = generation("deb-0.0.1", self.work / "release")
        release = directory / "dists/preview/Release"
        release.write_text(release.read_text().replace("Suite: preview", "Suite: stable"))
        self.refuses(rebind(receipt, directory), directory, "gpgv failed|InRelease differs")
        receipt, directory = generation("deb-0.0.1", self.work / "inrelease")
        shutil.copyfile(TESTDATA / "deb-0.0.2/dists/preview/InRelease", directory / "dists/preview/InRelease")
        self.refuses(rebind(receipt, directory), directory, "InRelease differs from the selected Release")
        receipt, directory = generation("deb-0.0.1", self.work / "detached")
        shutil.copyfile(TESTDATA / "deb-0.0.2/dists/preview/Release.gpg", directory / "dists/preview/Release.gpg")
        self.refuses(rebind(receipt, directory), directory, "gpgv failed")

    def test_signed_rpm_metadata_binds_every_listed_metadata_object(self):
        receipt, directory = generation("rpm-0.0.1", self.work / "primary")
        [primary] = directory.glob("repodata/*-primary.xml.gz")
        primary.write_bytes(primary.read_bytes() + b"changed")
        self.refuses(rebind(receipt, directory), directory, "selected size")
        receipt, directory = generation("rpm-0.0.1", self.work / "repomd")
        metadata = directory / "repodata/repomd.xml"
        metadata.write_bytes(metadata.read_bytes().replace(b"<revision>", b"<revision>1"))
        self.refuses(rebind(receipt, directory), directory, "gpgv failed")

    def test_rpm_packages_require_the_selected_signature(self):
        native_tools("rpm", "rpmkeys", "gpg")
        receipt, directory = generation("rpm-0.0.1", self.work / "signed")
        admit(receipt, directory, self.catalog, True)
        package = directory / receipt["packages"][0]["file"]
        shutil.copyfile(TESTDATA / "inputs" / package.name, package)
        self.refuses(rebind(receipt, directory), directory, "no OpenPGP signature")

    def test_object_paths_and_sizes_are_exact(self):
        receipt, directory = generation("deb-0.0.1", self.work)
        path = directory / receipt["objects"][0]["path"]
        for name in ["../escape", str(path), "dists/../" + receipt["objects"][0]["path"], ""]:
            with self.subTest(name=name), self.assertRaises(ValueError):
                selected_object(directory, name)
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        check_bytes(path, digest, path.stat().st_size)
        for size in [True, -1, path.stat().st_size - 1, path.stat().st_size + 1]:
            with self.subTest(size=size), self.assertRaises(ValueError):
                check_bytes(path, digest, size)


if __name__ == "__main__":
    unittest.main()

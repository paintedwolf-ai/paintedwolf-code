"""Native apt/dnf assembly and candidate retention sign only selected package identities."""

import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

from scripts.linux_release.candidate_repositories import retain
from scripts.linux_release.native_repositories import assemble
from scripts.linux_release.openpgp import ephemeral
from scripts.linux_release.repository_validation import admit
from scripts.linux_release.signing import digest
from scripts.verification_tests.distribution.native_repository_fixtures import (
    NATIVE, PACKAGE, TESTDATA, inputs, manifest, native_tools)

SOURCE = "f" * 40


class NativeRepositoryAssemblyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        native_tools("dpkg-deb", "apt-ftparchive", "rpm", "rpmsign", "rpmkeys", "createrepo_c", "gpg", "gpgv")
        cls.temporary = tempfile.TemporaryDirectory()
        cls.catalog = ephemeral(Path(cls.temporary.name) / "trust")
        cls.private = cls.catalog.parent / "rehearsal-secret.asc"

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.inputs = inputs(self.work)

    def assemble(self, family, selected, destination=None, **options):
        destination = destination or self.work / (family + "-generation")
        arguments = {"catalog": self.catalog, "private": self.private, "rehearsal": True, "package": PACKAGE, **options}
        return assemble(family, selected, self.inputs, destination, "preview", **arguments), destination

    def test_signed_generations_admit_and_retain_post_signing_package_identity(self):
        for family in ["deb", "rpm"]:
            with self.subTest(family=family):
                selected = manifest(family, self.inputs)
                receipt, directory = self.assemble(family, selected)
                admit(receipt, directory, self.catalog, True)
                [package] = receipt["packages"]
                self.assertEqual(package["input_sha256"], selected["packages"][0]["sha256"])
                self.assertEqual(package["sha256"], digest(directory / package["file"]))
                self.assertEqual(package["sha256"] != package["input_sha256"], family == "rpm")
                self.assertTrue(package["file"].startswith("pool/" + package["sha256"] + "/"))
                self.assertFalse((directory / ".signing").exists())
        release = (self.work / "deb-generation/dists/preview/Release").read_text()
        self.assertIn("Acquire-By-Hash: yes", release)
        self.assertEqual(json.loads(self.catalog.read_text())["rehearsal"], True)
        with self.assertRaisesRegex(ValueError, "cross-trust"):
            admit(receipt, directory, self.catalog)

    def test_native_identity_must_match_the_selected_receipt_before_output(self):
        for family, changes, package in [("deb", {"native_version": "0.0.2-1"}, PACKAGE),
                                         ("rpm", {"native_version": "0.0.1-2"}, PACKAGE),
                                         ("deb", {"architecture": "arm64"}, PACKAGE),
                                         ("rpm", {"architecture": "arm64"}, PACKAGE),
                                         ("deb", {}, "painted-wolf-code")]:
            with self.subTest(family=family, changes=changes, package=package):
                destination = self.work / "refused"
                with self.assertRaisesRegex(ValueError, "native package identity disagrees"):
                    self.assemble(family, manifest(family, self.inputs, **changes), destination, package=package)
                self.assertFalse(destination.exists())

    def test_tampered_linked_escaping_or_unsupported_inputs_refuse_before_output(self):
        destination = self.work / "refused"
        original = manifest("deb", self.inputs)
        for changes, reason in [({"sha256": "0" * 64}, "differs from the selected immutable input"),
                                ({"file": "../" + NATIVE["deb"]}, "sibling package file"),
                                ({"file": NATIVE["rpm"]}, "sibling package file"),
                                ({"architecture": "riscv64"}, "architecture is unsupported")]:
            with self.subTest(reason=reason), self.assertRaisesRegex(ValueError, reason):
                self.assemble("deb", manifest("deb", self.inputs, **changes), destination)
            self.assertFalse(destination.exists())
        package = self.inputs / NATIVE["deb"]
        shutil.move(package, self.work / "linked.deb")
        package.symlink_to(self.work / "linked.deb")
        with self.assertRaisesRegex(ValueError, "differs from the selected immutable input"):
            self.assemble("deb", original, destination)
        self.assertFalse(destination.exists())

    def test_generation_identity_channel_and_signing_key_are_explicit(self):
        selected = manifest("deb", self.inputs)
        destination = self.work / "refused"
        for changed, reason in [({**selected, "source_commit": "short"}, "exact source"),
                                ({**selected, "format_version": 2}, "exact source"),
                                ({**selected, "version": "../1.0.0"}, "exact source"),
                                ({**selected, "packages": selected["packages"] * 2}, "distinct packages"),
                                ({**selected, "packages": []}, "distinct packages")]:
            with self.subTest(reason=reason), self.assertRaisesRegex(ValueError, reason):
                self.assemble("deb", changed, destination)
            self.assertFalse(destination.exists())
        for family, channel in [("pacman", "preview"), ("deb", "nightly")]:
            with self.assertRaisesRegex(ValueError, "supported repository family"):
                assemble(family, selected, self.inputs, destination, channel, self.catalog, self.private, True, PACKAGE)
        with self.assertRaisesRegex(ValueError, "owner-only private key"):
            assemble("deb", selected, self.inputs, destination, "preview", self.catalog, None, True, PACKAGE)
        self.assertFalse(destination.exists())
        other = ephemeral(self.work / "other")
        with self.assertRaisesRegex(ValueError, "private key differs"):
            self.assemble("rpm", manifest("rpm", self.inputs), self.work / "wrong-key",
                          private=other.parent / "rehearsal-secret.asc")
        destination.mkdir()
        with self.assertRaises(FileExistsError):
            self.assemble("deb", selected, destination)


class CandidateRepositoryTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        native_tools("dpkg-deb", "apt-ftparchive", "rpm", "rpmsign", "rpmkeys", "createrepo_c", "gpg", "gpgv")

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.candidates = self.work / "candidates"
        self.candidates.mkdir()
        # A product version 0.0.1 candidate carries DEB version 0.0.1 and RPM version-release 0.0.1-1.
        tree = self.work / "deb-tree"
        subprocess.run(["dpkg-deb", "--raw-extract", str(TESTDATA / "inputs" / NATIVE["deb"]), str(tree)], check=True)
        control = tree / "DEBIAN/control"
        control.write_text(control.read_text().replace("Version: 0.0.1-1\n", "Version: 0.0.1\n"))
        subprocess.run(["dpkg-deb", "--build", "--root-owner-group", str(tree), str(self.candidates / "candidate.deb")],
                       check=True, stdout=subprocess.DEVNULL)
        shutil.copyfile(TESTDATA / "inputs" / NATIVE["rpm"], self.candidates / "candidate.rpm")

    def retain(self, destination=None, source=SOURCE, version="0.0.1", architectures=("amd64",)):
        return retain(self.candidates, destination or self.work / "retained", source, version, "preview", 14,
                      list(architectures), PACKAGE)

    def test_retained_final_bytes_and_public_trust_reverify_without_private_material(self):
        receipt = self.retain()
        output = self.work / "retained"
        self.assertEqual((receipt["source_commit"], receipt["release_build"], receipt["architectures"]),
                         (SOURCE, 14, ["amd64"]))
        self.assertEqual({row["family"] for row in receipt["packages"]}, {"deb", "rpm"})
        for row in receipt["packages"]:
            self.assertEqual(digest(output / row["path"]), row["sha256"])
            self.assertEqual(row["sha256"] != row["input_sha256"], row["family"] == "rpm")
        self.assertFalse([path for path in output.rglob("*") if "secret" in path.name])
        catalog = output / receipt["trust_catalog"]
        self.assertTrue(json.loads(catalog.read_text())["rehearsal"])
        for row in receipt["repositories"]:
            manifest = json.loads((output / row["path"] / "generation.json").read_text())
            admit(manifest, output / row["path"], catalog, True)
            with self.assertRaisesRegex(ValueError, "cross-trust"):
                admit(manifest, output / row["path"], catalog)
        self.assertEqual(json.loads((output / "candidate-repositories.json").read_text()), receipt)

    def test_candidate_identity_must_match_the_independent_selection(self):
        for options, reason in [({"version": "0.0.2"}, "independently selected product version"),
                                ({"source": "short"}, "exact candidate source"),
                                ({"architectures": ["arm64"]}, "explicitly selected coverage")]:
            with self.subTest(options=options), self.assertRaisesRegex(ValueError, reason):
                self.retain(**options)
            self.assertFalse((self.work / "retained").exists())

    def test_each_family_needs_one_candidate_per_selected_architecture(self):
        (self.candidates / "candidate.rpm").unlink()
        with self.assertRaisesRegex(ValueError, "one candidate per supported architecture"):
            self.retain()
        for name in ["second.rpm", "third.rpm", "candidate.rpm"]:
            shutil.copyfile(TESTDATA / "inputs" / NATIVE["rpm"], self.candidates / name)
        with self.assertRaisesRegex(ValueError, "one candidate per supported architecture"):
            self.retain()
        (self.candidates / "third.rpm").unlink()
        with self.assertRaisesRegex(ValueError, "explicitly selected coverage"):
            self.retain()
        self.assertFalse((self.work / "retained").exists())

    def test_output_must_be_new_and_outside_the_read_only_inputs(self):
        with self.assertRaisesRegex(ValueError, "outside its read-only input"):
            self.retain(self.candidates / "retained")
        existing = self.work / "existing"
        existing.mkdir()
        with self.assertRaisesRegex(ValueError, "new destination"):
            self.retain(existing)
        self.assertEqual(sorted(path.name for path in self.candidates.iterdir()), ["candidate.deb", "candidate.rpm"])


if __name__ == "__main__":
    unittest.main()

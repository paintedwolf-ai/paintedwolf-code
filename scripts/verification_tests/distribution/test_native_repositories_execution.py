"""Native apt/dnf assembly and candidate retention sign only selected package identities."""

import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

from scripts.linux_release import repository_withdrawal
from scripts.linux_release.candidate_repositories import retain
from scripts.linux_release.native_repositories import assemble, release_date, resign, same_restore
from scripts.linux_release.repository_objects import encoded
from scripts.linux_release.repository_r2 import publish_r2
from scripts.linux_release.openpgp import ephemeral
from scripts.linux_release.repository_validation import admit
from scripts.linux_release.signing import digest, sign
from scripts.verification_tests.distribution.native_repository_fixtures import (
    NATIVE, PACKAGE, TESTDATA, inputs, manifest, native_tools)
from scripts.verification_tests.distribution.repository_fixture_support import halt_distribution

SOURCE = "f" * 40
ORIGIN = "https://rehearsal.example.invalid"
PREFIX = "release-system-tests/apt-restore"


class Origin:
    """An isolated object store with the release transport's operations."""

    def __init__(self):
        self.objects = {}

    def current(self, key, directory=None):
        return json.loads(self.objects[key]) if key in self.objects else None

    def put(self, key, source, mutable=False):
        if not mutable and self.objects.get(key, source.read_bytes()) != source.read_bytes():
            raise ValueError("immutable collision")
        self.objects[key] = source.read_bytes()

    def public_bytes(self, url, source, mutable=False):
        if self.objects.get(url.removeprefix(ORIGIN + "/")) != source.read_bytes():
            raise ValueError("public bytes differ")

    def prefixes(self, prefix):
        return sorted(key.removesuffix("generation.json") for key in self.objects
                      if key.startswith(prefix) and key.endswith("/generation.json"))

    def fetch(self, base_url, row, directory):
        for item in row["receipt"]["objects"]:
            target = directory / item["path"]
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(self.objects[f"{PREFIX}/generations/{row['replacement']}/{item['path']}"])
        (directory / "generation.json").write_bytes(encoded(row["receipt"]))

    def read(self, base_url, row, generation, path):
        return self.objects[f"{PREFIX}/generations/{generation}/{path}"]


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

    def test_apt_indexes_list_platform_named_release_packages_by_their_architecture(self):
        released = "paintedwolf-repository-fixture_v0.0.1_linux-x86_64.deb"
        selected = manifest("deb", self.inputs, file=released)
        (self.inputs / NATIVE["deb"]).rename(self.inputs / released)
        receipt, directory = self.assemble("deb", selected)
        admit(receipt, directory, self.catalog, True)
        amd64 = (directory / "dists/preview/main/binary-amd64/Packages").read_text()
        self.assertIn("Package: " + PACKAGE + "\n", amd64)
        self.assertIn("Filename: " + receipt["packages"][0]["file"] + "\n", amd64)
        self.assertEqual(sorted(path.name for path in (directory / "dists/preview/main").iterdir()), ["binary-amd64"])

    def test_apt_restore_signs_a_later_release_over_unchanged_indexes(self):
        receipt, directory = self.assemble("deb", manifest("deb", self.inputs))
        original = (directory / "dists/preview/Release").read_bytes()
        halted = release_date(original)
        restored = self.work / "restored"
        shutil.copytree(directory, restored)
        resigned = resign(receipt, restored, self.catalog, self.private, True, after=halted)
        admit(resigned, restored, self.catalog, True)
        fresh = (restored / "dists/preview/Release").read_bytes()
        self.assertGreater(release_date(fresh), halted)
        undated = lambda raw: [line for line in raw.splitlines() if not line.startswith(b"Date: ")]
        self.assertEqual(undated(fresh), undated(original))
        same_restore(receipt, resigned)
        self.assertNotEqual(resigned["inventory_sha256"], receipt["inventory_sha256"])
        index = next(row for row in resigned["objects"] if row["path"].endswith("binary-amd64/Packages"))
        for changed in ({**resigned, "version": "0.0.2"},
                        {**resigned, "objects": [{**row, "sha256": "0" * 64} if row is index else row for row in resigned["objects"]]}):
            with self.assertRaisesRegex(ValueError, "differs from its retained generation"):
                same_restore(receipt, changed)
        rpm, rpm_directory = self.assemble("rpm", manifest("rpm", self.inputs))
        with self.assertRaisesRegex(ValueError, "retained apt offer"):
            resign(rpm, rpm_directory, self.catalog, self.private, True, after=halted)

    def test_halted_apt_channel_restores_its_retained_offer_under_a_fresh_release(self):
        origin = Origin()
        published = {}
        for version in ("1.2.2", "1.2.3"):
            receipt, directory = self.assemble("deb", manifest("deb", self.inputs, version=version), self.work / version)
            expected = published["1.2.2"]["generation"] if published else None
            published[version] = publish_r2(receipt, directory, self.catalog, PREFIX, ORIGIN, expected, True, transport=origin)
        options = {"rehearsal": True, "catalog": self.catalog, "storage_prefix": PREFIX}
        rows = repository_withdrawal.prepare(halt_distribution("1.2.3", "1.2.2", channels=("preview",)),
                                             base_url=ORIGIN, transport=origin, **options)
        [row] = [row for row in rows if row["action"] == "withdraw"]
        self.assertEqual((row["family"], row["replacement"], row["resign"]), ("deb", published["1.2.2"]["generation"], True))
        before = dict(origin.objects)
        with self.assertRaisesRegex(ValueError, "re-signed generation"):
            repository_withdrawal.apply(rows, ORIGIN, transport=origin, **options)
        self.assertEqual(origin.objects, before)
        signed = self.work / "signed"
        signed.mkdir()
        resigned = repository_withdrawal.resign(rows, ORIGIN, signed, self.private, fetch=origin.fetch,
                                                read=origin.read, **options)
        repository_withdrawal.apply(resigned, ORIGIN, signed=signed, transport=origin, **options)
        state = origin.current(f"{PREFIX}/channels/deb/preview/current.json")
        [restore] = [row for row in resigned if row["action"] == "withdraw"]
        self.assertEqual((state["operation"], state["version"], state["generation"], state["previous"]),
                         ("withdraw", "1.2.2", restore["replacement"], published["1.2.3"]["generation"]))
        halted = origin.objects[f"{PREFIX}/generations/{published['1.2.3']['generation']}/dists/preview/Release"]
        served = origin.objects[f"{PREFIX}/channels/deb/preview/dists/preview/InRelease"]
        self.assertGreater(release_date(served), release_date(halted))
        for name in ("Packages", "Packages.gz"):
            self.assertEqual(origin.objects[f"{PREFIX}/channels/deb/preview/dists/preview/main/binary-amd64/{name}"],
                             origin.objects[f"{PREFIX}/generations/{published['1.2.2']['generation']}/dists/preview/main/binary-amd64/{name}"])
        self.assertTrue(all(row["action"] == "keep" for row in repository_withdrawal.prepare(
            halt_distribution("1.2.3", "1.2.2", channels=("preview",)), base_url=ORIGIN, transport=origin, **options)))
        # A later halt selecting 1.2.2 again finds the original and its re-signed copy: one apt restore.
        receipt, directory = self.assemble("deb", manifest("deb", self.inputs, version="1.2.4"), self.work / "1.2.4")
        publish_r2(receipt, directory, self.catalog, PREFIX, ORIGIN, restore["replacement"], True, transport=origin)
        rows = repository_withdrawal.prepare(halt_distribution("1.2.4", "1.2.2", channels=("preview",)),
                                             base_url=ORIGIN, transport=origin, **options)
        [again] = [row for row in rows if row["action"] == "withdraw"]
        self.assertEqual(again["receipt"]["version"], "1.2.2")
        self.assertIn(again["replacement"], {restore["replacement"], restore["restores"]})
        self.assertTrue(again["resign"])
        # Any other retained generation naming 1.2.2 is a real ambiguity.
        other = {**restore["retained"], "source_commit": "e" * 40}
        origin.objects[f"{PREFIX}/generations/{hashlib.sha256(encoded(other)).hexdigest()}/generation.json"] = encoded(other)
        with self.assertRaises(repository_withdrawal.MissingRetainedGeneration) as failure:
            repository_withdrawal.prepare(halt_distribution("1.2.4", "1.2.2", channels=("preview",)),
                                          base_url=ORIGIN, transport=origin, **options)
        self.assertEqual(failure.exception.reason["code"], "NATIVE_REPLACEMENT_AMBIGUOUS")

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
        # A release RPM signed under the selected key enters the pool as the published bytes.
        signed = self.inputs / NATIVE["rpm"]
        sign("rpm", signed, signed, self.catalog, self.private, True)
        receipt, directory = self.assemble("rpm", manifest("rpm", self.inputs), self.work / "signed-generation")
        admit(receipt, directory, self.catalog, True)
        [package] = receipt["packages"]
        self.assertEqual((package["input_sha256"], package["sha256"]), (digest(signed), digest(signed)))
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

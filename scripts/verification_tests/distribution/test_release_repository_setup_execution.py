"""Repository setup bytes enter the ordinary release only after independent admission."""

import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release import release_repository_setup as setup
from scripts.linux_release.repository_objects import encoded
from scripts.linux_release.repository_operations import identity

SOURCE = "a" * 40
VERSION = "1.2.3"
ORIGIN = "https://downloads.example.invalid/linux"


class ReleaseRepositorySetupTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.generations = self.work / "generations"
        self.public = self.work / "public.asc"
        self.public.write_bytes(b"selected public key")
        self.signer = {"generation": 1, "fingerprint": "A" * 40}
        self.artifacts = self.work / "artifacts"
        self.artifacts.mkdir()
        self.mode = False
        self.manifests = {}
        for family in ("deb", "rpm"):
            directory = self.generations / family
            directory.mkdir(parents=True)
            packages = []
            for platform in ("linux-x86_64", "linux-aarch64"):
                package = self.artifacts / f"painted-wolf-code_v{VERSION}_{platform}.{family}"
                package.write_bytes((platform + family).encode())
                sha = hashlib.sha256(package.read_bytes()).hexdigest()
                (package.parent / (package.name + ".sha256")).write_text(sha + "\n")
                packages.append({"file": "pool/" + package.name, "sha256": sha})
            manifest = {"format_version": 1, "family": family, "channel": "stable", "version": VERSION,
                        "source_commit": SOURCE, "rehearsal": False, "packages": packages,
                        "inventory_sha256": "c" * 64, "signing_generation": 1, "key_fingerprint": "A" * 40}
            (directory / "generation.json").write_bytes(encoded(manifest))
            self.manifests[family] = manifest
        self.built = {}
        self.authorities = []

    def build(self, manifest, directory, catalog, destination, base_url, private, rehearsal):
        self.assertIs(rehearsal, self.mode)
        self.assertEqual(manifest, self.manifests[manifest["family"]])
        self.assertEqual(base_url, ORIGIN)
        self.assertEqual(private, self.work / "private")
        destination.mkdir()
        filename = "painted-wolf-code-repo." + manifest["family"]
        payload = (manifest["family"] + " package bytes").encode()
        (destination / filename).write_bytes(payload)
        (destination / "SHA256SUMS").write_bytes(b"signed checksums " + payload)
        (destination / "SHA256SUMS.asc").write_bytes(b"signature " + payload)
        (destination / "package-key.asc").write_bytes(self.public.read_bytes())
        result = {"package": {"file": filename, "sha256": hashlib.sha256(payload).hexdigest()},
                  "family": manifest["family"], "source_commit": SOURCE}
        self.built[manifest["family"]] = result
        return copy.deepcopy(result)

    def admit(self, package, checksums, signature, catalog, **authority):
        family = authority["family"]
        self.assertEqual(authority, dict(family=family, source_commit=SOURCE, version=VERSION, channel="stable",
                         repository_generation=identity(self.manifests[family]), inventory_sha256="c" * 64,
                         base_url=ORIGIN, signing_generation=1, key_fingerprint="A" * 40, rehearsal=self.mode))
        self.assertEqual(signature.read_bytes(), b"signature " + package.read_bytes())
        self.assertEqual(checksums.read_bytes(), b"signed checksums " + package.read_bytes())
        self.authorities.append(authority)
        return copy.deepcopy(self.built[family])

    def assemble(self, *, admit=None, rehearsal=False):
        with patch.object(setup, "selection", return_value=(self.signer, self.public)), \
             patch.object(setup.repository_setup, "build", side_effect=self.build), \
             patch.object(setup.repository_setup_admission, "admit_setup", side_effect=admit or self.admit):
            return setup.assemble(self.generations, self.work / "artifacts", self.work / "output", VERSION, SOURCE,
                                  "stable", ORIGIN, self.work / "private", rehearsal=rehearsal)

    def test_both_native_families_publish_exact_admitted_bytes_and_unambiguous_companions(self):
        receipts = self.assemble()
        output = self.work / "output"
        self.assertEqual({receipt["family"] for receipt in receipts}, {"deb", "rpm"})
        expected = {"linux-package-key.asc"}
        for family in ("deb", "rpm"):
            name = "painted-wolf-code-repo." + family
            expected.update(name + suffix for suffix in ("", ".SHA256SUMS", ".SHA256SUMS.asc", ".setup.json"))
            self.assertEqual((output / name).read_bytes(), (family + " package bytes").encode())
            self.assertEqual(json.loads((output / (name + ".setup.json")).read_bytes()), self.built[family])
        self.assertEqual({path.name for path in output.iterdir()}, expected)
        self.assertEqual((output / "linux-package-key.asc").read_bytes(), self.public.read_bytes())
        self.assertEqual(len(self.authorities), 2)

    def test_real_release_authority_refuses_mismatched_generation_and_package_bytes(self):
        path = self.generations / "deb/generation.json"
        original = copy.deepcopy(self.manifests["deb"])
        for field, value in (("source_commit", "d" * 40), ("version", "1.2.4"), ("channel", "preview"),
                             ("family", "rpm"), ("rehearsal", True), ("packages", [])):
            with self.subTest(field=field):
                changed = {**original, field: value}
                path.write_bytes(encoded(changed))
                with self.assertRaises(ValueError):
                    self.assemble()
                self.assertFalse((self.work / "output").exists())
        path.write_bytes(encoded(original))
        package = next(self.artifacts.glob("*.deb"))
        package.write_bytes(b"changed after final checksum")
        with self.assertRaisesRegex(ValueError, "published checksum"):
            self.assemble()
        self.assertFalse((self.work / "output").exists())

    def test_independent_setup_admission_refuses_changed_source_before_collection(self):
        def changed(*arguments, **authority):
            result = self.admit(*arguments, **authority)
            result["source_commit"] = "d" * 40
            return result
        with self.assertRaisesRegex(ValueError, "independently reconstructed"):
            self.assemble(admit=changed)
        self.assertFalse((self.work / "output").exists())

    def test_production_publisher_never_admits_rehearsal_mode(self):
        observed = []
        authority = setup.ReleaseAuthority(self.artifacts, VERSION, SOURCE, "stable", ORIGIN, observed.append)
        manifest = {**self.manifests["deb"], "rehearsal": True}
        with self.assertRaisesRegex(ValueError, "selected release"):
            authority({"catalog": setup.CATALOG, "storage_prefix": "linux", "base_url": ORIGIN,
                       "operation": "publish", "manifest": manifest})
        self.assertEqual(observed, [])

    def test_explicit_rehearsal_uses_same_authority_and_constructor_path(self):
        self.mode = True
        for family, manifest in self.manifests.items():
            manifest["rehearsal"] = True
            (self.generations / family / "generation.json").write_bytes(encoded(manifest))
        with self.assertRaisesRegex(ValueError, "selected release"):
            self.assemble()
        self.assertFalse((self.work / "output").exists())
        with self.assertRaisesRegex(ValueError, "trust mode"):
            self.assemble(rehearsal=1)
        receipts = self.assemble(rehearsal=True)
        self.assertEqual({row["family"] for row in receipts}, {"deb", "rpm"})
        self.assertEqual(len(self.authorities), 2)

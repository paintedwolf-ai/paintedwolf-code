"""Repository setup bytes enter the ordinary release only after independent admission."""

import copy
from contextlib import nullcontext, redirect_stderr, redirect_stdout
import io
import hashlib
import json
from pathlib import Path
import tempfile
import shutil
import sys
import unittest
from unittest.mock import patch

from scripts.linux_release import release_repository_setup as setup
from scripts.linux_release.repository_objects import encoded
from scripts.linux_release.repository_operations import identity
from scripts.linux_release.repository_setup import package_identity
from scripts.release_distribution_contract.channels import select

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
            directory = self.generations / "stable" / family
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
        from scripts.release_distribution_contract.channels import select
        self.plan = self.work / 'channel-plan.json'
        from update_keys import load_registry
        self.plan.write_text(json.dumps(select('stable', VERSION, load_registry()['signing_generation'], SOURCE,
            read=lambda key: {'version': '1.3.0-rc.1'} if '/preview/' in key else None)))
        self.built = {}
        self.authorities = []

    def build(self, manifest, directory, catalog, destination, base_url, private, rehearsal):
        self.assertIs(rehearsal, self.mode)
        self.assertEqual(manifest, {**self.manifests[manifest["family"]], "channel": manifest["channel"]})
        self.assertEqual(base_url, ORIGIN)
        self.assertEqual(private, self.work / "private")
        destination.mkdir()
        filename = package_identity(VERSION, manifest["family"], manifest["channel"], 1)["file"]
        payload = (manifest["family"] + " package bytes").encode()
        (destination / filename).write_bytes(payload)
        (destination / "SHA256SUMS").write_bytes(b"signed checksums " + payload)
        (destination / "SHA256SUMS.asc").write_bytes(b"signature " + payload)
        (destination / "package-key.asc").write_bytes(self.public.read_bytes())
        result = {"package": {"file": filename, "sha256": hashlib.sha256(payload).hexdigest()},
                  "family": manifest["family"], "source_commit": SOURCE, "channel": manifest["channel"]}
        self.built[manifest["family"]] = result
        return copy.deepcopy(result)

    def admit(self, package, checksums, signature, catalog, **authority):
        family = authority["family"]
        self.assertEqual(authority, dict(family=family, source_commit=SOURCE, version=VERSION, channel=authority["channel"],
                         repository_generation=identity({**self.manifests[family], "channel": authority["channel"]}), inventory_sha256="c" * 64,
                         base_url=ORIGIN, signing_generation=1, key_fingerprint="A" * 40, rehearsal=self.mode))
        self.assertEqual(signature.read_bytes(), b"signature " + package.read_bytes())
        self.assertEqual(checksums.read_bytes(), b"signed checksums " + package.read_bytes())
        self.authorities.append(authority)
        return copy.deepcopy(self.built[family])

    def assemble(self, *, admit=None, rehearsal=False, channels=None):
        with patch.object(setup, "selection", return_value=(self.signer, self.public)), \
             patch.object(setup.repository_setup, "build", side_effect=self.build), \
             patch.object(setup.repository_setup_admission, "admit_setup", side_effect=admit or self.admit):
            return setup.assemble(self.generations, self.work / "artifacts", self.work / "output", VERSION, SOURCE,
                                  channels or ["stable"], ORIGIN, self.work / "private", rehearsal=rehearsal)

    def test_both_native_families_publish_exact_admitted_bytes_and_unambiguous_companions(self):
        receipts = self.assemble()
        output = self.work / "output"
        self.assertEqual({receipt["family"] for receipt in receipts}, {"deb", "rpm"})
        expected = {"linux-package-key.asc"}
        for family in ("deb", "rpm"):
            name = package_identity(VERSION, family, "stable", 1)["file"]
            expected.update(name + suffix for suffix in ("", ".SHA256SUMS", ".SHA256SUMS.asc", ".setup.json"))
            self.assertEqual((output / name).read_bytes(), (family + " package bytes").encode())
            self.assertEqual(json.loads((output / (name + ".setup.json")).read_bytes()), self.built[family])
        self.assertEqual({path.name for path in output.iterdir()}, expected)
        self.assertEqual((output / "linux-package-key.asc").read_bytes(), self.public.read_bytes())
        self.assertEqual(len(self.authorities), 2)

    def test_shared_stable_promotion_collects_distinct_setup_packages_for_both_channels(self):
        selected = select('stable', VERSION, 1, SOURCE, read=lambda key: None)
        for family, manifest in self.manifests.items():
            directory = self.generations / 'preview' / family
            shutil.copytree(self.generations / 'stable' / family, directory)
            (directory / 'generation.json').write_bytes(encoded({**manifest, 'channel': 'preview'}))
        receipts = self.assemble(channels=selected['channels'])
        self.assertEqual({(row['family'], row['channel']) for row in receipts},
                         {(family, channel) for family in ('deb', 'rpm') for channel in selected['channels']})
        self.assertEqual(len({row['package']['file'] for row in receipts}), 4)
        for row in receipts:
            name = row['package']['file']
            self.assertTrue((self.work / 'output' / name).is_file())
            self.assertEqual(json.loads((self.work / 'output' / (name + '.setup.json')).read_bytes()), row)

    def test_real_release_authority_refuses_mismatched_generation_and_package_bytes(self):
        path = self.generations / "stable/deb/generation.json"
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
        authority = setup.ReleaseAuthority(self.artifacts, VERSION, SOURCE, "stable", "https://downloads.paintedwolf.dev", observed.append)
        manifest = {**self.manifests["deb"], "rehearsal": True}
        with self.assertRaisesRegex(ValueError, "selected release"):
            authority({"catalog": setup.CATALOG, "storage_prefix": "linux", "base_url": "https://downloads.paintedwolf.dev",
                       "operation": "publish", "manifest": manifest})
        self.assertEqual(observed, [])

    def test_same_publication_authority_binds_explicit_rehearsal_without_production_widening(self):
        protected = self.work / 'disposable-keys.json'
        protected.write_text(json.dumps({'format_version': 1, 'rehearsal': True, 'signing_generation': 1,
            'generations': [{'generation': 1, 'fingerprint': 'A' * 40, 'public_key': self.public.name}]}))
        origin, namespace = 'https://rehearsal.example.invalid', 'release-system-tests/authority'
        observed = []
        authority = setup.ReleaseAuthority(self.artifacts, VERSION, SOURCE, 'stable', origin, observed.append,
            rehearsal=True, catalog=protected, storage_prefix=namespace)
        request = {'manifest': {**self.manifests['deb'], 'rehearsal': True}, 'catalog': protected,
                   'storage_prefix': namespace, 'base_url': origin, 'operation': 'publish'}
        self.assertIs(authority(request), True)
        self.assertEqual(observed, [SOURCE])
        for changed in ({'storage_prefix': 'linux'}, {'manifest': self.manifests['deb']}, {'catalog': setup.CATALOG}):
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                authority({**request, **changed})
        self.assertEqual(observed, [SOURCE])
        production = setup.ReleaseAuthority(self.artifacts, VERSION, SOURCE, 'stable',
                                            'https://downloads.paintedwolf.dev', observed.append)
        with self.assertRaises(ValueError):
            production(request)
        with self.assertRaisesRegex(ValueError, 'trust mode'):
            setup.ReleaseAuthority(self.artifacts, VERSION, SOURCE, 'stable', origin, observed.append, rehearsal=1)

    def test_explicit_rehearsal_uses_same_authority_and_constructor_path(self):
        self.mode = True
        for family, manifest in self.manifests.items():
            manifest["rehearsal"] = True
            (self.generations / "stable" / family / "generation.json").write_bytes(encoded(manifest))
        with self.assertRaisesRegex(ValueError, "selected release"):
            self.assemble()
        self.assertFalse((self.work / "output").exists())
        with self.assertRaisesRegex(ValueError, "trust mode"):
            self.assemble(rehearsal=1)
        receipts = self.assemble(rehearsal=True)
        self.assertEqual({row["family"] for row in receipts}, {"deb", "rpm"})
        self.assertEqual(len(self.authorities), 2)

    def test_release_cli_uses_selected_inputs_and_production_trust(self):
        arguments = ["release_repository_setup", "--generations", str(self.generations),
                     "--artifacts", str(self.artifacts), "--output", str(self.work / "output"),
                     "--version", VERSION, "--source-commit", SOURCE, "--channel", "stable", "--base-url", ORIGIN, "--plan", str(self.plan)]
        output = io.StringIO()
        with patch.object(sys, "argv", arguments), \
             patch.object(setup, "private_key", return_value=nullcontext(self.work / "private")), \
             patch.object(setup, "selection", return_value=(self.signer, self.public)), \
             patch.object(setup.repository_setup, "build", side_effect=self.build), \
             patch.object(setup.repository_setup_admission, "admit_setup", side_effect=self.admit), \
             redirect_stdout(output):
            setup.main()
        self.assertEqual({row["family"] for row in json.loads(output.getvalue())}, {"deb", "rpm"})
        self.assertTrue((self.work / "output/linux-package-key.asc").is_file())

    def test_release_cli_refuses_without_public_linux_before_loading_key(self):
        arguments = ["release_repository_setup", "--generations", str(self.generations),
                     "--artifacts", str(self.artifacts), "--output", str(self.work / "output"),
                     "--version", VERSION, "--source-commit", SOURCE, "--channel", "stable", "--base-url", ORIGIN, "--plan", str(self.plan)]
        with patch.object(sys, "argv", arguments), patch.object(setup, "public_linux", return_value=[]), \
             patch.object(setup, "private_key") as key, redirect_stderr(io.StringIO()), \
             self.assertRaises(SystemExit) as error:
            setup.main()
        self.assertEqual(error.exception.code, 1)
        key.assert_not_called()
        self.assertFalse((self.work / "output").exists())

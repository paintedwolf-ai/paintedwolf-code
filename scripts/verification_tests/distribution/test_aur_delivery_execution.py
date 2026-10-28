"""AUR availability needs explicit operation, fresh admission and exact recipe bytes."""

import copy
import hashlib
import os
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release import aur_withdrawal
from scripts.linux_release.aur_delivery import FixturePublisher, RecipePublisher, plan, production_publish, protected_publisher, recipe_objects
from scripts.linux_release.aur_delivery_workflow import main, prepare, request, selected_directory, verify_provenance
from scripts.linux_release.aur_observation import observe
from scripts.linux_release.aur_origin import FixtureOrigin, ReadOnlyOrigin
from scripts.linux_release.aur_release_authority import authorize
from scripts.linux_release.aur_withdrawal import OPERATION, authority, recipe as withdrawal_recipe, srcinfo_fields, withdraw
from scripts.linux_release.repository_objects import encoded
from scripts.verification_tests.distribution.pacman_repository_fixtures import PACKAGE, TESTDATA, native_tools, trust

SOURCE = "f" * 40
CONTROLLER = {"id": 7, "repository": {"id": 1}, "path": ".github/workflows/release.yml", "head_branch": "main",
              "head_sha": SOURCE, "event": "workflow_dispatch", "status": "completed", "conclusion": "success"}


def recipe(directory: Path, version: str) -> Path:
    directory.mkdir(parents=True)
    (directory / "PKGBUILD").write_text(f"pkgname=painted-wolf-code-preview-bin\npkgver={version}\n")
    (directory / ".SRCINFO").write_text(f"pkgbase = painted-wolf-code-preview-bin\n\tpkgver = {version}\n")
    return directory


class AurAuthorityTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = recipe(Path(self.temporary.name) / "recipe", "1.0.0")
        self.manifest = {'family': 'pacman', 'source_commit': SOURCE, 'version': '1.0.0',
                         'channel': 'stable', 'rehearsal': False, 'architecture': 'x86_64',
                         'inventory_sha256': 'b' * 64, 'signing_generation': 1, 'key_fingerprint': 'A' * 40}
        self.metadata = {**self.manifest, 'package': 'painted-wolf-code-bin'}
        self.artifacts = b'{"final":"selected"}\n'
        self.selected = plan(self.metadata, [self.manifest], self.root, hashlib.sha256(self.artifacts).hexdigest())
        self.qualified = []

    def admit(self, selected=None, generations=None):
        """The shared exact-source Qualification lookup is the only injected boundary."""
        return authorize(self.selected if selected is None else selected, self.root,
                         artifacts_bytes=self.artifacts,
                         generations=[self.manifest] if generations is None else generations,
                         source_commit=SOURCE, version='1.0.0', channel='stable',
                         qualified=self.qualified.append)

    def test_qualification_is_reread_after_the_recipe_binding(self):
        self.assertTrue(self.admit())
        self.assertEqual(self.qualified, [SOURCE])

        def unqualified(_source):
            raise ValueError("no passing exact-commit qualification")
        with self.assertRaisesRegex(ValueError, "qualification"):
            authorize(self.selected, self.root, artifacts_bytes=self.artifacts, generations=[self.manifest],
                      source_commit=SOURCE, version='1.0.0', channel='stable', qualified=unqualified)
        with self.assertRaisesRegex(ValueError, "protected controller reevaluation"):
            production_publish({'accepted': True})

    def test_selected_source_signer_final_files_and_exact_recipes_are_bound(self):
        for mutation in [{'source_commit': 'e' * 40}, {'operation': 'publish'}, {'rehearsal': 0},
                         {'final_artifacts_sha256': 'c' * 64}, {'recipe_sha256': 'd' * 64}]:
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                self.admit({**self.selected, **mutation})
        for field, value in [('signing_generation', True), ('architecture', 'aarch64')]:
            changed = copy.deepcopy(self.selected)
            changed['generations'][0][field] = value
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, "every published pacman"):
                self.admit(changed)
        for generations in [[], [{**self.manifest, 'rehearsal': True}], [{**self.manifest, 'version': '0.9.0'}]]:
            with self.subTest(generations=generations), self.assertRaisesRegex(ValueError, "every published pacman"):
                self.admit(generations=generations)
        (self.root / 'PKGBUILD').write_text('changed after review\n')
        with self.assertRaisesRegex(ValueError, "independently reviewed delivery plan"):
            self.admit()
        self.assertEqual(self.qualified, [])

    def test_recipe_plan_refuses_wrong_source_and_symlink_declarations(self):
        with self.assertRaisesRegex(ValueError, "selected binary generations"):
            plan({**self.metadata, 'source_commit': 'e' * 40}, [self.manifest], self.root, 'a' * 64)
        with self.assertRaisesRegex(ValueError, "final-artifact inventory"):
            plan(self.metadata, [self.manifest], self.root, 'short')
        (self.root / 'PKGBUILD').unlink()
        (self.root / 'PKGBUILD').symlink_to(self.root / '.SRCINFO')
        with self.assertRaisesRegex(ValueError, "regular files"):
            plan(self.metadata, [self.manifest], self.root, 'a' * 64)

    def test_public_transport_requires_private_files_and_never_disables_host_verification(self):
        key, hosts = self.root.parent / 'key', self.root.parent / 'known-hosts'
        for path in [key, hosts]:
            path.write_text('fixture only\n')
            path.chmod(0o600)
        publisher = protected_publisher(self.root, 'painted-wolf-code-bin', key, hosts, lambda *_: None)
        self.assertEqual(publisher.origin, 'ssh://aur@aur.archlinux.org/painted-wolf-code-bin.git')
        self.assertIn('StrictHostKeyChecking=yes', publisher.environment['GIT_SSH_COMMAND'])
        with self.assertRaisesRegex(ValueError, "trust mode"):
            publisher.authorize({**self.selected, 'rehearsal': True}, self.root)
        key.chmod(0o644)
        with self.assertRaisesRegex(ValueError, "owner-only"):
            protected_publisher(self.root, 'painted-wolf-code-bin', key, hosts, lambda *_: None)
        with self.assertRaisesRegex(ValueError, "maintained AUR application package"):
            protected_publisher(self.root, 'unreviewed-name', key, hosts, lambda *_: None)


class AurFixtureDeliveryTests(unittest.TestCase):
    """A real bare Git origin owned by the test receives, reads back and refuses recipe commits."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        (self.work / "publisher").mkdir()
        self.publisher = FixturePublisher(self.work / "publisher")

    def selected(self, version, rehearsal=True):
        manifest = {"family": "pacman", "source_commit": SOURCE, "version": version, "channel": "preview",
                    "rehearsal": rehearsal, "architecture": "x86_64", "inventory_sha256": "b" * 64,
                    "signing_generation": 1, "key_fingerprint": "A" * 40}
        metadata = {**manifest, "package": "painted-wolf-code-preview-bin"}
        directory = recipe(self.work / ("recipe-" + version + ("" if rehearsal else "-production")), version)
        return plan(metadata, [manifest], directory, "c" * 64), directory

    def head(self):
        return self.publisher.head()

    def test_delivery_reads_back_exact_bytes_and_retries_without_another_commit(self):
        self.assertIsNone(self.head())
        selected, directory = self.selected("0.0.1")
        delivered = self.publisher.publish(selected, directory, None)
        self.assertEqual((delivered["commit"], delivered["availability"]), (self.head(), "origin-readback"))
        retried = self.publisher.publish(selected, directory, None)
        self.assertEqual(retried["commit"], delivered["commit"])
        clone = self.work / "clone"
        subprocess.run(["git", "clone", "--quiet", self.publisher.origin, str(clone)], check=True)
        self.assertEqual((clone / "PKGBUILD").read_bytes(), (directory / "PKGBUILD").read_bytes())
        message = subprocess.check_output(["git", "-C", str(clone), "log", "-1", "--format=%B"], text=True)
        self.assertEqual(json.loads(message), {**selected, "previous": None})
        advanced, advanced_directory = self.selected("0.0.2")
        second = self.publisher.publish(advanced, advanced_directory, delivered["commit"])
        self.assertNotEqual(second["commit"], delivered["commit"])
        self.assertEqual(self.publisher.publish(advanced, advanced_directory, delivered["commit"])["commit"],
                         second["commit"])

    def test_stale_changed_regressing_and_production_deliveries_leave_the_origin_unchanged(self):
        selected, directory = self.selected("0.0.2")
        with self.assertRaisesRegex(ValueError, "changed since the reviewed"):
            self.publisher.publish(selected, directory, "0" * 40)
        self.assertIsNone(self.head())
        first = self.publisher.publish(selected, directory, None)["commit"]
        older, older_directory = self.selected("0.0.1")
        with self.assertRaisesRegex(ValueError, "must advance"):
            self.publisher.publish(older, older_directory, first)
        newer, newer_directory = self.selected("0.0.3")
        with self.assertRaisesRegex(ValueError, "changed since the reviewed"):
            self.publisher.publish(newer, newer_directory, "0" * 40)
        (newer_directory / "PKGBUILD").write_text("changed after selection\n")
        with self.assertRaisesRegex(ValueError, "changed after delivery selection"):
            self.publisher.publish(newer, newer_directory, first)
        production, production_directory = self.selected("0.0.3", rehearsal=False)
        with self.assertRaisesRegex(ValueError, "explicitly selected rehearsal"):
            self.publisher.publish(production, production_directory, first)
        with self.assertRaisesRegex(ValueError, "separate explicit operation"):
            self.publisher.publish({**newer, "operation": "publish"}, newer_directory, first)
        self.assertEqual(self.head(), first)


class AurWorkflowAdmissionTests(unittest.TestCase):
    """The delivery workflow's input, provenance and bundle admission through its declared CLI."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.bundle = self.work / "inputs"
        self.bundle.mkdir()
        for name in ["pacman-0.0.1-x86_64", "pacman-0.0.1-aarch64"]:
            shutil.copytree(TESTDATA / name, self.bundle / name)
            path = self.bundle / name / "generation.json"
            path.write_text(json.dumps({**json.loads(path.read_text()), "rehearsal": False}))
        (self.bundle / "final-artifacts.json").write_text(json.dumps({"source_commit": SOURCE}))
        self.delivery({"format_version": 1, "source_commit": SOURCE, "base_url": "https://repository.example.invalid",
                       "generations": ["pacman-0.0.1-x86_64", "pacman-0.0.1-aarch64"]})
        self.receipt = self.work / "output/delivery.json"

    def prepare(self, catalog, operation="prepare", receipt=None):
        return prepare(self.bundle, SOURCE, receipt or self.receipt, operation, catalog=catalog, package=PACKAGE)

    def delivery(self, value):
        (self.bundle / "delivery.json").write_text(json.dumps(value))

    def cli(self, *arguments):
        try:
            main(list(arguments))
        except SystemExit as stopped:
            return stopped.code
        return 0

    def test_workflow_inputs_are_checked_before_any_controller_read(self):
        request("123", SOURCE, "prepare")
        request("1", SOURCE, "publish-aur")
        for run, source, operation, reason in [("0", SOURCE, "prepare", "numeric controller run"),
                                               ("12; rm -rf /", SOURCE, "prepare", "numeric controller run"),
                                               ("1", "HEAD", "prepare", "exact release source"),
                                               ("1", SOURCE.upper(), "prepare", "exact release source"),
                                               ("1", SOURCE, "publish", "separately authorized")]:
            with self.subTest(run=run, source=source, operation=operation), self.assertRaisesRegex(ValueError, reason):
                request(run, source, operation)
        self.assertEqual(self.cli("request", "--release-run-id", "x", "--source-commit", SOURCE,
                                  "--operation", "prepare"), 1)

    def test_only_the_selected_successful_main_release_controller_run_is_admitted(self):
        verify_provenance(CONTROLLER, 1, 7, SOURCE)
        for mutation in [{'head_branch': 'feature'}, {'conclusion': 'failure'}, {'event': 'pull_request'},
                         {'path': '.github/workflows/other.yml'}, {'repository': {'id': 2}}, {'id': 8},
                         {'status': 'in_progress'}]:
            with self.subTest(mutation=mutation), self.assertRaisesRegex(ValueError, "successful protected main"):
                verify_provenance({**CONTROLLER, **mutation}, 1, 7, SOURCE)
        run = self.work / "run.json"
        run.write_text(json.dumps(CONTROLLER))
        provenance = ["provenance", "--run", str(run), "--repository-id", "1"]
        self.assertEqual(self.cli(*provenance, "--release-run-id", "7", "--source-commit", SOURCE), 0)
        self.assertEqual(self.cli(*provenance, "--release-run-id", "8", "--source-commit", SOURCE), 1)

    def test_controller_run_must_be_dispatched_for_the_selected_source_commit(self):
        for head in ["e" * 40, SOURCE.upper(), None]:
            with self.subTest(head=head), self.assertRaisesRegex(ValueError, "selected source commit"):
                verify_provenance({**CONTROLLER, "head_sha": head}, 1, 7, SOURCE)
        stale = self.work / "stale-run.json"
        stale.write_text(json.dumps({**CONTROLLER, "head_sha": "e" * 40}))
        self.assertEqual(self.cli("provenance", "--run", str(stale), "--repository-id", "1", "--release-run-id", "7",
                                  "--source-commit", SOURCE), 1)

    def test_bundle_must_bind_the_selected_source_and_relative_generations(self):
        catalog = trust(self.work / "trust", rehearsal=False)
        for change, reason in [({"source_commit": "e" * 40}, "selected controller request"),
                               ({"extra": True}, "selected controller request"),
                               ({"format_version": True}, "selected controller request"),
                               ({"generations": ["../outside"]}, "generation path is invalid"),
                               ({"generations": [str(self.bundle / "pacman-0.0.1-x86_64")]}, "generation path is invalid")]:
            with self.subTest(change=change):
                value = json.loads((self.bundle / "delivery.json").read_text())
                self.delivery({**value, **change})
                with self.assertRaisesRegex(ValueError, reason):
                    self.prepare(catalog)
                self.delivery(value)
        (self.bundle / "linked").symlink_to(self.bundle / "pacman-0.0.1-x86_64")
        value = json.loads((self.bundle / "delivery.json").read_text())
        self.delivery({**value, "generations": ["linked"]})
        with self.assertRaisesRegex(ValueError, "escapes its selected root"):
            self.prepare(catalog)
        self.assertFalse(self.receipt.exists())

    def test_publication_receipt_and_final_inventory_are_closed_before_generation(self):
        catalog = trust(self.work / "trust", rehearsal=False)
        with self.assertRaisesRegex(ValueError, "protected controller reevaluation"):
            self.prepare(catalog, operation="publish-aur")
        with self.assertRaisesRegex(ValueError, "cannot replace its retained controller inputs"):
            self.prepare(catalog, receipt=self.bundle / "delivery.json")
        generation = self.bundle / "pacman-0.0.1-aarch64/generation.json"
        original = generation.read_text()
        generation.write_text(json.dumps({**json.loads(original), "source_commit": "e" * 40}))
        with self.assertRaisesRegex(ValueError, "differs from the selected source"):
            self.prepare(catalog)
        generation.write_text(original)
        final = self.bundle / "final-artifacts.json"
        final.write_text(json.dumps({"source_commit": "e" * 40}))
        with self.assertRaisesRegex(ValueError, "final-artifact inventory differs from the selected source"):
            self.prepare(catalog)
        final.write_text(json.dumps({"source_commit": SOURCE}))
        final.rename(self.work / "final.json")
        final.symlink_to(self.work / "final.json")
        with self.assertRaisesRegex(ValueError, "final-artifact inventory"):
            self.prepare(catalog)
        self.assertFalse(self.receipt.exists())
        self.assertEqual(self.cli("prepare", "--input-root", str(self.bundle), "--source-commit", SOURCE,
                                  "--operation", "publish-aur", "--receipt", str(self.receipt)), 1)

    def test_prepared_receipt_binds_the_generated_recipe_and_final_inventory(self):
        native_tools("makepkg", "bsdtar")
        catalog = trust(self.work / "trust", rehearsal=False)
        selected = self.prepare(catalog)
        receipt = json.loads(self.receipt.read_text())
        self.assertEqual(receipt, {**selected, "availability": "prepared-only", "aur_published": False})
        self.assertEqual(selected["final_artifacts_sha256"],
                         hashlib.sha256((self.bundle / "final-artifacts.json").read_bytes()).hexdigest())
        self.assertEqual({row["path"] for row in selected["recipe_objects"]}, {"PKGBUILD", ".SRCINFO"})
        self.assertEqual({row["architecture"] for row in selected["generations"]}, {"x86_64", "aarch64"})
        self.assertEqual(selected["package"], "paintedwolf-repository-fixture-preview-bin")


def selection(root):
    recipe_dir = root / "recipe"
    recipe_dir.mkdir()
    (recipe_dir / "PKGBUILD").write_text("pkgname=painted-wolf-code-bin\npkgver=1.0.0\n")
    (recipe_dir / ".SRCINFO").write_text("pkgbase = painted-wolf-code-bin\n\tpkgver = 1.0.0\n")
    objects = recipe_objects(recipe_dir)
    selected = {"format_version": 1, "operation": "publish-aur", "source_commit": "a" * 40,
                "version": "1.0.0", "channel": "stable", "package": "painted-wolf-code-bin",
                "rehearsal": True, "final_artifacts_sha256": "b" * 64, "generations": [],
                "recipe_objects": objects, "recipe_sha256": hashlib.sha256(encoded(objects)).hexdigest()}
    return recipe_dir, selected


class MemoryOrigin:
    production = False
    origin = "/fixture-only"

    def __init__(self, selected, recipe_dir):
        self.commit = "c" * 40
        self.heads = [self.commit, self.commit]
        self.value = {"commit": self.commit, "message": encoded({**selected, "previous": None}),
                      "objects": {row["path"]: (recipe_dir / row["path"]).read_bytes()
                                  for row in selected["recipe_objects"]}}

    def head(self):
        return self.heads.pop(0)

    def read(self, commit):
        return copy.deepcopy(self.value)


class AvailabilityTests(unittest.TestCase):
    def test_fresh_observation_binds_recipe_and_complete_inventory_without_production_claim(self):
        with tempfile.TemporaryDirectory() as temporary:
            recipe_dir, selected = selection(Path(temporary))
            origin = MemoryOrigin(selected, recipe_dir)
            calls = []
            def authorize_call(plan_val, actual):
                calls.append((copy.deepcopy(plan_val), actual))
                return {"accepted": True}
            report = observe(selected, recipe_dir, origin.commit, backend=origin, authorize=authorize_call)
            self.assertEqual(len(calls), 2)
            self.assertFalse(report["production_available"])
            self.assertEqual(report["facts"]["source_sha256"], selected["recipe_sha256"])
            self.assertEqual(report["facts"]["package_sha256"], selected["final_artifacts_sha256"])
            self.assertEqual(report["identity_semantics"]["package_sha256"], "complete-final-artifact-inventory")

    def test_drift_tampering_forged_plan_and_extra_files_cannot_advertise_availability(self):
        for failure in ("head", "blob", "message", "extra"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temporary:
                recipe_dir, selected = selection(Path(temporary))
                origin = MemoryOrigin(selected, recipe_dir)
                if failure == "head":
                    origin.heads[-1] = "d" * 40
                elif failure == "blob":
                    origin.value["objects"]["PKGBUILD"] += b"tampered"
                elif failure == "message":
                    origin.value["message"] = encoded({**selected, "version": "9.0.0", "previous": None})
                else:
                    origin.value["objects"]["unexpected.install"] = b"foreign"
                with self.assertRaises(ValueError):
                    observe(selected, recipe_dir, origin.commit, backend=origin,
                            authorize=lambda *_: {"accepted": True})

    def test_second_admission_and_local_recipe_must_remain_fresh(self):
        with tempfile.TemporaryDirectory() as temporary:
            recipe_dir, selected = selection(Path(temporary))
            origin = MemoryOrigin(selected, recipe_dir)
            calls = []
            def authorize_call(*_):
                calls.append(1)
                return {"accepted": len(calls) == 1}
            with self.assertRaisesRegex(ValueError, "admission changed"):
                observe(selected, recipe_dir, origin.commit, backend=origin, authorize=authorize_call)

    def test_fixture_origin_cannot_supply_production_authority(self):
        with tempfile.TemporaryDirectory() as temporary:
            recipe_dir, selected = selection(Path(temporary))
            origin = MemoryOrigin(selected, recipe_dir)
            selected["rehearsal"] = False
            with self.assertRaisesRegex(ValueError, "authority cannot be exchanged"):
                observe(selected, recipe_dir, origin.commit, backend=origin, authorize=lambda *_: {"accepted": True})

    def test_protected_origin_is_fixed_and_never_accepts_an_operator_url(self):
        with self.assertRaisesRegex(ValueError, "maintained"):
            ReadOnlyOrigin(Path("/unused"), "foreign-package")


@unittest.skipUnless(sys.platform.startswith("linux"), "requires the protected Linux Git process lifecycle")
class RealLocalGitTests(unittest.TestCase):
    def test_independent_readback_of_real_local_publisher_commit_and_cleanup(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            recipe_dir, selected = selection(root)
            publishing = root / "publisher"
            publishing.mkdir(mode=0o700)
            publisher = FixturePublisher(publishing)
            Path(publisher.origin).chmod(0o700)
            delivered = publisher.publish(selected, recipe_dir, None)
            reader_root = root / "reader"
            with FixtureOrigin(reader_root, Path(publisher.origin)) as backend:
                report = observe(selected, recipe_dir, delivered["commit"], backend=backend,
                                 authorize=lambda *_: {"accepted": True})
            self.assertFalse(reader_root.exists())
            self.assertFalse(report["production_available"])
            self.assertEqual(report["facts"]["commit"], delivered["commit"])
            self.assertEqual(publisher.head(), delivered["commit"])

    def test_moved_real_origin_never_reuses_a_retained_old_availability_receipt(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            recipe_dir, selected = selection(root)
            publishing = root / "publisher"
            publishing.mkdir(mode=0o700)
            publisher = FixturePublisher(publishing)
            Path(publisher.origin).chmod(0o700)
            first = publisher.publish(selected, recipe_dir, None)
            later = {**selected, "version": "1.0.1"}
            publisher.publish(later, recipe_dir, first["commit"])
            with FixtureOrigin(root / "reader", Path(publisher.origin)) as backend:
                with self.assertRaisesRegex(ValueError, "no longer offers"):
                    observe(selected, recipe_dir, first["commit"], backend=backend,
                            authorize=lambda *_: {"accepted": True})

    def test_committed_symlink_is_refused_without_checking_out_or_executing_recipe(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            recipe_dir, selected = selection(root)
            publishing = root / "publisher"
            publishing.mkdir(mode=0o700)
            publisher = FixturePublisher(publishing)
            Path(publisher.origin).chmod(0o700)
            first = publisher.publish(selected, recipe_dir, None)
            checkout = publishing / "checkout-1"
            (checkout / "PKGBUILD").unlink()
            (checkout / "PKGBUILD").symlink_to(".SRCINFO")
            publisher.git("-C", checkout, "add", "--", "PKGBUILD")
            publisher.git("-C", checkout, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                          "commit", "-m", encoded({**selected, "previous": first["commit"]}).decode().strip())
            publisher.git("-C", checkout, "push", "origin", "HEAD:refs/heads/master")
            changed = publisher.head()
            reader = root / "reader"
            with FixtureOrigin(reader, Path(publisher.origin)) as backend:
                with self.assertRaisesRegex(ValueError, "nonregular"):
                    observe(selected, recipe_dir, changed, backend=backend,
                            authorize=lambda *_: {"accepted": True})
            self.assertFalse(reader.exists())


ROOT = Path(__file__).resolve().parents[3]
SRCINFO = ("pkgbase = painted-wolf-code-bin\n\tpkgdesc = Painted Wolf Code\n\tpkgver = 1.0.3\n\tpkgrel = 1\n"
           "\turl = https://paintedwolf.ai\n\tarch = x86_64\n\tarch = aarch64\n\tlicense = MIT\n"
           "\tsource_x86_64 = https://example.invalid/a.pkg.tar.zst\n\n"
           "pkgname = painted-wolf-code-bin\n")
RECORD = {"operation": "publish-aur", "source_commit": SOURCE, "version": "1.0.3", "channel": "stable",
          "package": "painted-wolf-code-bin", "rehearsal": False}


def published(directory: Path, version: str) -> dict:
    """A published recipe and its delivery plan as the release lane leaves them."""
    directory.mkdir(parents=True)
    (directory / "PKGBUILD").write_text(f"pkgname=painted-wolf-code-bin\npkgver={version}\npkgrel=1\n")
    (directory / ".SRCINFO").write_text(SRCINFO.replace("1.0.3", version))
    manifest = {"family": "pacman", "source_commit": SOURCE, "version": version, "channel": "stable",
                "rehearsal": True, "architecture": "x86_64", "inventory_sha256": "b" * 64,
                "signing_generation": 1, "key_fingerprint": "A" * 40}
    return plan({**manifest, "package": "painted-wolf-code-bin"}, [manifest], directory, "c" * 64)


class WithdrawalRecipeTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)

    def test_refusing_revision_keeps_identity_and_advances_only_pkgrel(self):
        selected = withdrawal_recipe(RECORD, SRCINFO.encode(), self.work / "recipe", "d" * 40)
        pkgbuild = (self.work / "recipe/PKGBUILD").read_text()
        self.assertIn("pkgname=painted-wolf-code-bin\npkgver=1.0.3\npkgrel=2\n", pkgbuild)
        self.assertIn("arch=(aarch64 x86_64)\nlicense=(MIT)\n", pkgbuild)
        self.assertNotIn("source", pkgbuild)
        self.assertRegex(pkgbuild, r"package\(\) \{\n  error '[^\n]*1\.0\.3 was withdrawn[^\n]*'\n  return 1\n\}\n$")
        fields = srcinfo_fields((self.work / "recipe/.SRCINFO").read_bytes())
        self.assertEqual(fields, {"pkgbase": ["painted-wolf-code-bin"], "pkgdesc": ["Painted Wolf Code (withdrawn release)"],
                                  "pkgver": ["1.0.3"], "pkgrel": ["2"], "url": ["https://paintedwolf.ai"],
                                  "arch": ["aarch64", "x86_64"], "license": ["MIT"], "options": ["!strip", "!debug"],
                                  "pkgname": ["painted-wolf-code-bin"]})
        self.assertEqual({key: selected[key] for key in RECORD if key != "operation"},
                         {key: RECORD[key] for key in RECORD if key != "operation"})
        self.assertEqual((selected["operation"], selected["withdrawn_commit"], selected["pkgrel"]), (OPERATION, "d" * 40, 2))
        self.assertEqual(selected["recipe_objects"], recipe_objects(self.work / "recipe"))
        self.assertEqual(selected["recipe_sha256"], hashlib.sha256(encoded(selected["recipe_objects"])).hexdigest())
        epoch = SRCINFO.replace("\tpkgrel = 1\n", "\tpkgrel = 1.2\n\tepoch = 3\n")
        withdrawal_recipe(RECORD, epoch.encode(), self.work / "epoch", "d" * 40)
        self.assertIn("pkgrel=2\nepoch=3\n", (self.work / "epoch/PKGBUILD").read_text())
        self.assertIn("\tpkgrel = 2\n\tepoch = 3\n", (self.work / "epoch/.SRCINFO").read_text())

    def test_makepkg_reads_the_refusing_revision_identically_and_refuses_to_package(self):
        native_tools("makepkg")
        if os.geteuid() == 0:
            self.skipTest("makepkg refuses to run as root")
        withdrawal_recipe(RECORD, SRCINFO.encode(), self.work / "recipe", "d" * 40)
        printed = subprocess.check_output(["makepkg", "--printsrcinfo"], cwd=self.work / "recipe")
        self.assertEqual(srcinfo_fields(printed), srcinfo_fields((self.work / "recipe/.SRCINFO").read_bytes()))
        built = subprocess.run(["makepkg", "--force", "--nodeps", "--noconfirm"], cwd=self.work / "recipe",
                               capture_output=True, text=True, timeout=120)
        self.assertNotEqual(built.returncode, 0)
        self.assertIn("1.0.3 was withdrawn", built.stdout + built.stderr)
        self.assertEqual([path.name for path in (self.work / "recipe").glob("*.pkg.tar*")], [])

    def test_foreign_or_malformed_published_declarations_refuse(self):
        for index, (changed, pattern) in enumerate([
                (SRCINFO.replace("pkgbase = painted-wolf-code-bin", "pkgbase = painted-wolf-code-preview-bin"), "not the halted"),
                (SRCINFO.replace("pkgname = painted-wolf-code-bin", "pkgname = other"), "not the halted"),
                (SRCINFO.replace("\tpkgrel = 1\n", "\tpkgrel = 0\n"), "pkgrel"),
                (SRCINFO.replace("\tpkgrel = 1\n", ""), "pkgrel"),
                (SRCINFO.replace("\tpkgver = 1.0.3\n", "\tpkgver = 1.0.3\n\tpkgver = 1.0.4\n"), "pkgver"),
                (SRCINFO.replace("\tarch = x86_64\n\tarch = aarch64\n", ""), "architecture"),
                (SRCINFO.replace("\turl = https://paintedwolf.ai\n", "\tURL: nope\n"), "invalid declaration"),
                (SRCINFO, "not the halted")]):
            record = {**RECORD, "package": "painted-wolf-code-preview-bin"} if index == 7 else RECORD
            with self.subTest(index=index), self.assertRaisesRegex(ValueError, pattern):
                withdrawal_recipe(record, changed.encode(), self.work / str(index), "d" * 40)
            self.assertFalse((self.work / str(index)).exists())


class WithdrawalDeliveryTests(unittest.TestCase):
    """A real bare Git origin owned by the test receives the refusing revision exactly once."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        (self.work / "publisher").mkdir()
        self.publisher = FixturePublisher(self.work / "publisher")
        self.clones = 0

    def factory(self, root, package, key, hosts, authorize):
        """The stable package shares the test origin; the preview package owns an empty one."""
        self.assertEqual((key, hosts), (Path("key"), Path("hosts")))
        if package == "painted-wolf-code-preview-bin":
            return FixturePublisher(root)

        def authorized(selected, recipe_dir):
            FixturePublisher.fixture_authority(selected, recipe_dir)
            authorize(selected, recipe_dir)
        return RecipePublisher(root, self.publisher.origin, authorized)

    def shared_factory(self, root, package, key, hosts, authorize):
        return RecipePublisher(root, self.publisher.origin, FixturePublisher.fixture_authority)

    def withdraw(self, bad, name, **options):
        return withdraw(bad, self.work / name, Path("key"), Path("hosts"), publisher_factory=self.factory, **options)

    def clone(self):
        self.clones += 1
        clone = self.work / ("clone-" + str(self.clones))
        subprocess.run(["git", "clone", "--quiet", self.publisher.origin, str(clone)], check=True)
        return clone

    def commits(self):
        clone = self.clone()
        return int(subprocess.check_output(["git", "-C", str(clone), "rev-list", "--count", "HEAD"], text=True))

    def test_withdrawal_advances_the_offered_version_once_and_leaves_other_channels(self):
        selected = published(self.work / "recipe-1.0.3", "1.0.3")
        first = self.publisher.publish(selected, self.work / "recipe-1.0.3", None)["commit"]
        result = self.withdraw("1.0.3", "halt")
        self.assertEqual(result["preview"], {"package": "painted-wolf-code-preview-bin", "availability": "no-recipe",
                                             "withdrawn": False})
        stable = result["stable"]
        self.assertTrue(stable["withdrawn"])
        self.assertEqual((stable["withdrawn_commit"], stable["previous"], stable["availability"]),
                         (first, first, "origin-readback"))
        self.assertNotEqual(stable["commit"], first)
        clone = self.clone()
        pkgbuild = (clone / "PKGBUILD").read_text()
        self.assertIn("pkgver=1.0.3\npkgrel=2\n", pkgbuild)
        self.assertIn("return 1", pkgbuild)
        self.assertIn("\tpkgrel = 2\n", (clone / ".SRCINFO").read_text())
        message = json.loads(subprocess.check_output(["git", "-C", str(clone), "log", "-1", "--format=%B"], text=True))
        self.assertEqual(message, {key: value for key, value in stable.items()
                                   if key not in {"commit", "availability", "withdrawn"}})
        self.assertEqual(self.commits(), 2)
        # An origin whose record names another package is tampering, not another version.
        with self.assertRaisesRegex(ValueError, "foreign package"):
            withdraw("1.0.3", self.work / "foreign", Path("key"), Path("hosts"), publisher_factory=self.shared_factory)
        again = self.withdraw("1.0.3", "halt-again")
        self.assertEqual((again["stable"]["commit"], again["stable"]["withdrawn"]), (stable["commit"], True))
        self.assertEqual(self.commits(), 2)
        # A withdrawn recipe is never withdrawn again through the publisher either.
        current = self.publisher.inspect()
        twice = withdrawal_recipe(current["record"], current["srcinfo"], self.work / "twice", current["commit"])
        with self.assertRaisesRegex(ValueError, "must replace the published recipe"):
            self.publisher.withdraw(twice, self.work / "twice", current["commit"])
        self.assertEqual(self.commits(), 2)

    def test_other_offered_versions_dry_runs_and_changed_revisions_leave_the_origin_unchanged(self):
        selected = published(self.work / "recipe-1.0.4", "1.0.4")
        first = self.publisher.publish(selected, self.work / "recipe-1.0.4", None)["commit"]
        result = self.withdraw("1.0.3", "halt")
        self.assertEqual(result["stable"], {"package": "painted-wolf-code-bin", "availability": "other-version",
                                            "withdrawn": False, "offered": "1.0.4"})
        dry = self.withdraw("1.0.4", "dry", dry_run=True)
        self.assertEqual((dry["stable"]["availability"], dry["stable"]["withdrawn"], dry["stable"]["pkgrel"]),
                         ("prepared-only", False, 2))
        self.assertTrue((self.work / "dry/stable/recipe/PKGBUILD").is_file())
        self.assertEqual(self.publisher.head(), first)
        with self.assertRaisesRegex(ValueError, "invalid SemVer"):
            self.withdraw("not-a-version", "invalid")
        current = self.publisher.inspect()
        self.assertEqual(current["commit"], first)
        revision = withdrawal_recipe(current["record"], current["srcinfo"], self.work / "revision", first)
        with self.assertRaisesRegex(ValueError, "explicit operation"):
            self.publisher.withdraw({**revision, "operation": "publish-aur"}, self.work / "revision", first)
        with self.assertRaisesRegex(ValueError, "explicit operation"):
            self.publisher.withdraw(revision, self.work / "revision", None)
        with self.assertRaisesRegex(ValueError, "changed since the reviewed"):
            self.publisher.withdraw(revision, self.work / "revision", "0" * 40)
        (self.work / "revision/PKGBUILD").write_text("changed after selection\n")
        with self.assertRaisesRegex(ValueError, "changed after delivery selection"):
            self.publisher.withdraw(revision, self.work / "revision", first)
        self.assertEqual(self.publisher.head(), first)
        self.assertEqual(self.commits(), 1)

    def test_authority_binds_the_halted_version_channel_package_and_exact_revision(self):
        revision = withdrawal_recipe({**RECORD, "version": "1.0.4"}, SRCINFO.replace("1.0.3", "1.0.4").encode(),
                          self.work / "revision", "d" * 40)
        authorize = authority("1.0.4", "stable", "painted-wolf-code-bin")
        self.assertIsNone(authorize(revision, self.work / "revision"))
        for changed in ({"version": "1.0.3"}, {"channel": "preview"}, {"package": "painted-wolf-code-preview-bin"},
                        {"operation": "publish-aur"}):
            with self.subTest(changed=changed), self.assertRaisesRegex(ValueError, "differs from the halted release"):
                authorize({**revision, **changed}, self.work / "revision")
        with self.assertRaisesRegex(ValueError, "differs from its prepared revision"):
            authorize({**revision, "recipe_sha256": "0" * 64}, self.work / "revision")
        (self.work / "revision/PKGBUILD").write_text("changed\n")
        with self.assertRaisesRegex(ValueError, "differs from its prepared revision"):
            authorize(revision, self.work / "revision")


class WithdrawalCommandTests(unittest.TestCase):
    def test_command_writes_the_withdrawal_record_and_passes_dry_run(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "halt"
            with patch.object(aur_withdrawal, "withdraw", return_value={"stable": {"withdrawn": False}}) as withdrawn:
                aur_withdrawal.main(["--bad", "1.0.3", "--private-key", "key", "--known-hosts", "hosts",
                                     "--output", str(output), "--dry-run"])
            self.assertEqual(json.loads((output / "withdrawal.json").read_text()), {"stable": {"withdrawn": False}})
            self.assertEqual(withdrawn.call_args.args, ("1.0.3", output, Path("key"), Path("hosts")))
            self.assertEqual(withdrawn.call_args.kwargs, {"dry_run": True})
            with patch.object(aur_withdrawal, "withdraw", side_effect=ValueError("origin changed")) as withdrawn:
                with self.assertRaises(SystemExit) as refused:
                    aur_withdrawal.main(["--bad", "1.0.3", "--private-key", "key", "--known-hosts", "hosts",
                                         "--output", str(output)])
            self.assertEqual((refused.exception.code, withdrawn.call_args.kwargs), (1, {"dry_run": False}))


class HaltSurfaceTests(unittest.TestCase):
    def test_halt_withdraws_every_delivery_surface_the_release_lane_publishes(self):
        workflows = ROOT / ".github/workflows"
        lane = (workflows / "release.yml").read_text() + (workflows / "release-linux-delivery.yml").read_text()
        halt = (workflows / "release-halt.yml").read_text()
        surfaces = {"scripts.linux_release.release_repositories publish": "scripts.linux_release.repository_withdrawal apply",
                    "scripts.nix_cache.release_lane publish": "scripts.nix_cache.release_lane withdraw",
                    "release:publish:aur-recipe -- publish": "scripts.linux_release.aur_withdrawal --bad"}
        for publication, withdrawal in surfaces.items():
            self.assertIn(publication, lane)
            self.assertIn(withdrawal, halt)
        writes = set(re.findall(r"scripts\.(?:linux_release|nix_cache)\.[a-z_]+ publish\b|release:publish:[a-z-]+ -- publish\b", lane))
        self.assertEqual(writes, set(surfaces))
        # The AUR revision is pushed by the recipe owner from the halt's own protected environment.
        aur = halt[halt.index("  withdraw-aur:"):]
        for required in ("environment: release-aur", "needs: halt", "AUR_SSH_PRIVATE_KEY: ${{ secrets.AUR_SSH_PRIVATE_KEY }}",
                         "packaging/linux/aur-known-hosts", "--dry-run", "-u pw-aur"):
            self.assertIn(required, aur)


if __name__ == "__main__":
    unittest.main()

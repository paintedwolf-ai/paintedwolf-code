"""AUR availability needs explicit operation, fresh admission and exact recipe bytes."""

import copy
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from scripts.linux_release.aur_delivery import FixturePublisher, plan, production_publish, protected_publisher
from scripts.linux_release.aur_delivery_workflow import main, prepare, request, selected_directory, verify_provenance
from scripts.linux_release.aur_release_authority import authorize
from scripts.linux_release.repository_operations import identity
from scripts.verification_tests.distribution.pacman_repository_fixtures import PACKAGE, TESTDATA, native_tools, trust

SOURCE = "f" * 40
CONTROLLER = {"id": 7, "repository": {"id": 1}, "path": ".github/workflows/release.yml", "head_branch": "main",
              "event": "workflow_dispatch", "status": "completed", "conclusion": "success"}


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
        self.report = {'accepted': True, 'verified_repositories': [
            {'family': 'pacman', 'generation': identity(self.manifest), 'inventory_sha256': 'b' * 64}]}

    def admit(self, selected=None, report=None):
        """The release admission owner lands separately; its verdict is the only injected boundary."""
        calls = []

        def verify(*args, **kwargs):
            calls.append((args, kwargs))
            return self.report if report is None else report
        admission = SimpleNamespace(verify_release=verify)
        with patch.dict('sys.modules', {'scripts.linux_release.release_admission': admission}):
            result = authorize(self.selected if selected is None else selected, self.root,
                               catalog={}, artifacts_bytes=self.artifacts, campaign={},
                               evidence_root=self.root, file_root=self.root,
                               repositories={'generations': [{'manifest': self.manifest}]},
                               source_commit=SOURCE, version='1.0.0', channel='stable',
                               signing_generation=1, key_fingerprint='A' * 40, release_build=14)
        return result, calls

    def test_fresh_campaign_reevaluation_precedes_recipe_authority(self):
        result, calls = self.admit()
        self.assertTrue(result['accepted'])
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0][0][1], json.loads(self.artifacts))
        self.assertEqual((calls[0][1]['signing_generation'], calls[0][1]['release_build']), (1, 14))
        with self.assertRaisesRegex(ValueError, "freshly admitted"):
            self.admit(report={'accepted': False})
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
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, "every independently admitted"):
                self.admit(changed)
        (self.root / 'PKGBUILD').write_text('changed after review\n')
        with self.assertRaisesRegex(ValueError, "independently reviewed delivery plan"):
            self.admit()

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
        (self.bundle / "final-artifacts.json").write_text('{"final": "selected"}\n')
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
        verify_provenance(CONTROLLER, 1, 7)
        for mutation in [{'head_branch': 'feature'}, {'conclusion': 'failure'}, {'event': 'pull_request'},
                         {'path': '.github/workflows/other.yml'}, {'repository': {'id': 2}}, {'id': 8},
                         {'status': 'in_progress'}]:
            with self.subTest(mutation=mutation), self.assertRaisesRegex(ValueError, "successful protected main"):
                verify_provenance({**CONTROLLER, **mutation}, 1, 7)
        run = self.work / "run.json"
        run.write_text(json.dumps(CONTROLLER))
        self.assertEqual(self.cli("provenance", "--run", str(run), "--repository-id", "1", "--release-run-id", "7"), 0)
        self.assertEqual(self.cli("provenance", "--run", str(run), "--repository-id", "1", "--release-run-id", "8"), 1)

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


if __name__ == "__main__":
    unittest.main()

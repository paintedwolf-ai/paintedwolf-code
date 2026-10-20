"""Protected non-Linux provenance is independent from a passing Linux campaign."""
import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from scripts.linux_release.publication.release_inputs import ARTIFACT, SelectedReleaseInputs, select


class ReleaseInputSelectionTests(unittest.TestCase):
    def setUp(self):
        self.run = {"id": 12, "repository": {"id": 7, "full_name": "owner/repo"},
                    "head_repository": {"id": 7}, "head_sha": "a" * 40, "run_attempt": 2,
                    "path": ".github/workflows/release.yml", "head_branch": "main",
                    "event": "workflow_dispatch", "status": "completed", "conclusion": "success"}
        self.artifact = {"id": 31, "name": ARTIFACT, "expired": False, "size_in_bytes": 100,
                         "digest": "sha256:" + "b" * 64, "workflow_run": {
                             "id": 12, "head_sha": "a" * 40, "repository_id": 7, "head_repository_id": 7}}

    def api(self, endpoint):
        return self.run if endpoint.endswith("/runs/12") else {"artifacts": [self.artifact]}

    def test_exact_successful_protected_run_and_artifact(self):
        result = select(self.api, "owner/repo", 7, 12, 31)
        self.assertEqual((result["run_attempt"], result["artifact_id"], result["artifact_sha256"]), (2, 31, "b" * 64))

    def test_fork_pr_tag_failed_running_and_wrong_workflow_are_refused(self):
        for field, value in (("head_repository", {"id": 8}), ("event", "pull_request"),
                             ("head_branch", "v1.2.3"), ("conclusion", "failure"),
                             ("status", "in_progress"), ("path", ".github/workflows/other.yml"),
                             ("run_attempt", True)):
            with self.subTest(field=field):
                original = copy.deepcopy(self.run)
                self.run[field] = value
                with self.assertRaises(ValueError):
                    select(self.api, "owner/repo", 7, 12, 31)
                self.run = original

    def test_exact_id_digest_and_expiry_are_not_replaced_by_name(self):
        with self.assertRaises(ValueError):
            select(self.api, "owner/repo", 7, 12, 32)
        for field, value in (("expired", True), ("digest", None), ("name", "linux-final-admission-inputs")):
            original = copy.deepcopy(self.artifact)
            self.artifact[field] = value
            with self.assertRaises(ValueError):
                select(self.api, "owner/repo", 7, 12, 31)
            self.artifact = original


class ReleaseInputBytesTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        (self.root / "artifacts").mkdir()
        self.provenance = {"repository_id": 7, "run_id": 12, "run_attempt": 2, "controller_commit": "a" * 40}
        self.source = {"source_commit": "b" * 40, "source_tree": "c" * 40, "version": "1.2.3"}
        producer = {"format_version": 1, **self.provenance,
                    "source_commit": self.source["source_commit"], "source_tree": self.source["source_tree"]}
        self.write("producer.json", producer)
        self.catalog = {"schema_version": 2, "platforms": [
            {"updater_key": "darwin-aarch64", "publication": "public", "package_extensions": ["dmg"],
             "updater_extension": "app.tar.gz"},
            {"updater_key": "linux-x86_64", "publication": "public", "package_extensions": ["deb"],
             "updater_extension": None}]}
        self.write("catalog.json", self.catalog)
        self.package = "painted-wolf-code_v1.2.3_darwin-aarch64.dmg"
        self.updater_name = "painted-wolf-code_v1.2.3_darwin-aarch64.app.tar.gz"
        self.put(self.package, b"actual fixture package")
        self.put(self.package + ".sha256", (self.digest(b"actual fixture package") + "\n").encode())
        self.put(self.updater_name, b"actual fixture updater")
        self.put(self.updater_name + ".sig", b"protected-producer-signature\n")
        self.updater = {"version": "1.2.3", "notes": "Release notes", "pub_date": "2026-10-20T00:00:00Z",
                        "platforms": {"darwin-aarch64": {"signature": "protected-producer-signature",
                            "url": "https://download.example/releases/v1.2.3/" + self.updater_name}}}
        self.write("updater.json", self.updater)
        self.document = {"source": {"commit": "b" * 40, "tree": "c" * 40,
                            "catalog_sha256": self.digest((self.root / "catalog.json").read_bytes())},
                         "controller_commit": "d" * 40, "version": "1.2.3", "notes": "Release notes",
                         "pub_date": self.updater["pub_date"], "origin": "https://download.example",
                         "updater_manifest_sha256": self.digest((self.root / "updater.json").read_bytes()),
                         "platforms": {"darwin-aarch64": {"packages": {"dmg": {
                             "file": self.package, "sha256": self.digest(b"actual fixture package"), "bytes": len(b"actual fixture package")}}},
                             "linux-x86_64": {"packages": {}}}}
        self.binding = {"document": self.document, "catalog": self.root / "catalog.json",
                        "updater_manifest": self.root / "updater.json", "artifacts": self.root / "artifacts"}

    @staticmethod
    def digest(raw):
        return hashlib.sha256(raw).hexdigest()

    def write(self, name, value):
        (self.root / name).write_text(json.dumps(value))

    def put(self, name, raw):
        (self.root / "artifacts" / name).write_bytes(raw)

    def selected(self):
        return SelectedReleaseInputs(self.root, self.provenance, self.source, "d" * 40)

    def test_returns_scoped_facts_never_a_blanket_authorization(self):
        result = self.selected().authorize(self.binding)
        self.assertIsInstance(result, dict)
        self.assertIsNot(result, True)
        self.assertEqual(set(result["non_linux_packages"]), {"darwin-aarch64"})
        self.assertNotIn("repositories", result)

    def test_mutated_metadata_even_in_both_locations_cannot_self_authorize(self):
        selected = self.selected()
        self.updater["notes"] = self.document["notes"] = "Changed together"
        self.write("updater.json", self.updater)
        self.document["updater_manifest_sha256"] = self.digest((self.root / "updater.json").read_bytes())
        with self.assertRaisesRegex(ValueError, "changed after"):
            selected.authorize(self.binding)

    def test_mutated_updater_bytes_cannot_hide_behind_unchanged_signature_text(self):
        selected = self.selected()
        self.put(self.updater_name, b"different bytes")
        with self.assertRaisesRegex(ValueError, "changed after"):
            selected.authorize(self.binding)

    def test_source_controller_package_and_origin_substitution_refuse(self):
        for key, value in (("controller_commit", "e" * 40), ("origin", "https://foreign.example")):
            selected = self.selected()
            original = self.document[key]
            self.document[key] = value
            with self.assertRaises(ValueError):
                selected.authorize(self.binding)
            self.document[key] = original
        self.document["platforms"]["darwin-aarch64"]["packages"]["dmg"]["bytes"] = 1
        with self.assertRaises(ValueError):
            self.selected().authorize(self.binding)

    def test_missing_extra_symlink_and_bad_checksum_refuse(self):
        path = self.root / "artifacts" / self.package
        raw = path.read_bytes()
        path.unlink()
        path.symlink_to(self.root / "updater.json")
        with self.assertRaises(OSError):
            self.selected()
        path.unlink()
        path.write_bytes(raw)
        self.put("extra.bin", b"unexpected")
        with self.assertRaises(ValueError):
            self.selected()
        (self.root / "artifacts/extra.bin").unlink()
        self.put(self.package + ".sha256", b"wrong checksum")
        with self.assertRaises(ValueError):
            self.selected()

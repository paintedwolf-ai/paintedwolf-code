"""Corpus identity, integrity, and extraction checks."""

import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

SPEC = importlib.util.spec_from_file_location("fixture_files", Path(__file__).resolve().parents[2].joinpath("upgrade-fixture-files.py"))
fixture_files = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(fixture_files)


def sha(value):
    return hashlib.sha256(value).hexdigest()


class CorpusIntegrityTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.fixture = self.root / "corpus"
        self.fixture.mkdir()
        (self.fixture / "project-root").mkdir()
        self.identity = {"revision": 1, "shape": "a" * 64}
        self.files = {"store.db": b"valid database", "app-state-v1/prefs.json": b"{}"}
        self.kinds = {}
        self.metadata = {"app_version": "1.0.0-rc.1", "schema_version": 1, "schema_identity": self.identity}
        (self.fixture / "store.db").write_bytes(self.files["store.db"])
        (self.fixture / "SEMANTICS.json").write_text("{}")
        (self.fixture / "app-state-v1").mkdir()
        (self.fixture / "app-state-v1/prefs.json").write_text("{}")
        self.write_archive()

    def write_archive(self, mutate=None):
        manifest = {"app_version": "1.0.0-rc.1", "format_version": 1,
                    "schema_user_version": 1, "schema_shape_digest": self.identity["shape"],
                    "files": [{"rel_path": name, "kind": self.kinds.get(name, "file"),
                               "mode": 0o600, "size": len(body), "sha256": sha(body)}
                              for name, body in self.files.items()]}
        if mutate:
            mutate(manifest)
        with zipfile.ZipFile(self.fixture / "backup.zip", "w") as archive:
            archive.writestr("manifest.json", json.dumps(manifest))
            for name, body in self.files.items():
                archive.writestr(name, body)
        for name, key in (("backup.zip", "backup_sha256"), ("store.db", "store_sha256"),
                          ("SEMANTICS.json", "semantics_sha256")):
            self.metadata[key] = fixture_files.digest(self.fixture / name)
        (self.fixture / "MANIFEST.json").write_text(json.dumps(self.metadata))

    def diagnostics(self, command, **_):
        if command[2] == "schema-baseline":
            return json.dumps(self.identity)
        return json.dumps({"compatible": Path(command[3]).read_bytes() == b"valid database"})

    def test_candidate_checks_independent_archive_store_and_preserves_inputs(self):
        before = {p: p.read_bytes() for p in self.fixture.rglob("*") if p.is_file()}
        with patch.object(fixture_files.subprocess, "check_output", side_effect=self.diagnostics) as run:
            fixture_files.candidate(self.fixture, Path("engine"), "1.0.0-rc.1")
            self.assertEqual(run.call_count, 3)
        self.assertTrue(all(p.read_bytes() == value for p, value in before.items()))
        self.files["store.db"] = b"unsupported database"
        self.write_archive()
        with patch.object(fixture_files.subprocess, "check_output", side_effect=self.diagnostics):
            with self.assertRaisesRegex(ValueError, "archive store"):
                fixture_files.candidate(self.fixture, Path("engine"), "1.0.0-rc.1")

    def test_same_product_version_cannot_hide_a_stale_schema(self):
        changed = {**self.identity, "shape": "b" * 64}
        with patch.object(fixture_files.subprocess, "check_output", return_value=json.dumps(changed)):
            with self.assertRaisesRegex(ValueError, "schema is stale"):
                fixture_files.candidate(self.fixture, Path("engine"), "1.0.0-rc.1")

    def test_forged_metadata_cannot_hide_an_unsupported_store(self):
        (self.fixture / "store.db").write_bytes(b"unsupported database")
        self.write_archive()
        with patch.object(fixture_files.subprocess, "check_output", side_effect=self.diagnostics):
            with self.assertRaisesRegex(ValueError, "fixture store"):
                fixture_files.candidate(self.fixture, Path("engine"), "1.0.0-rc.1")

    def test_checksum_failure_does_not_materialize_anything(self):
        (self.fixture / "SEMANTICS.json").write_text('{"changed":true}')
        destination = self.root / "extracted"
        with self.assertRaisesRegex(ValueError, "checksum differs"):
            fixture_files.materialize(self.fixture, destination)
        self.assertFalse(destination.exists())

    def test_archive_entry_checksum_is_independently_verified(self):
        self.write_archive(lambda manifest: manifest["files"][0].update(sha256="0" * 64))
        with self.assertRaisesRegex(ValueError, "archive checksum"):
            fixture_files.checked_archive(self.fixture)

    def test_unsafe_archive_paths_are_rejected_before_writes(self):
        for name in ("../escape", "/absolute", "./alias", "nested//alias", "C:\\escape", "."):
            with self.subTest(name=name):
                with self.assertRaises(ValueError):
                    fixture_files.relative_path(name)
        self.files["link"] = b"../outside"
        self.kinds["link"] = "symlink"
        self.files["link/child"] = b"payload"
        self.write_archive()
        with self.assertRaisesRegex(ValueError, "through another entry"):
            fixture_files.materialize(self.fixture, self.root / "extracted")
        self.assertFalse((self.root / "outside").exists())

    def test_duplicate_manifest_paths_are_rejected(self):
        self.write_archive(lambda manifest: manifest["files"].append(manifest["files"][0]))
        with self.assertRaisesRegex(ValueError, "duplicate"):
            fixture_files.checked_archive(self.fixture)

    def test_preferences_overlay_cannot_follow_an_archived_symlink(self):
        del self.files["app-state-v1/prefs.json"]
        (self.fixture / "app-state-v1/prefs.json").unlink()
        self.files["app-state-v1"] = b"../outside"
        self.kinds["app-state-v1"] = "symlink"
        self.write_archive()
        destination = self.root / "extracted"
        with self.assertRaisesRegex(ValueError, "preferences root"):
            fixture_files.materialize(self.fixture, destination)
        self.assertFalse(destination.exists())

    def test_materialization_preserves_payloads_and_never_merges_existing_data(self):
        destination = self.root / "extracted"
        fixture_files.materialize(self.fixture, destination)
        self.assertEqual((destination / "store.db").read_bytes(), b"valid database")
        with self.assertRaisesRegex(ValueError, "must be empty"):
            fixture_files.materialize(self.fixture, destination)


class CorpusInventoryTests(unittest.TestCase):
    def test_only_selected_and_older_versions_are_admitted(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for version in ("1.0.1", "1.0.2-rc.2", "1.0.2-rc.10", "scratch"):
                (root / version).mkdir()
            fixture_files.check_inventory(root, "1.0.2-rc.10")
            with self.assertRaisesRegex(ValueError, "future-version corpus"):
                fixture_files.check_inventory(root, "1.0.2-rc.2")
            (root / "1.0.2").mkdir()
            with self.assertRaisesRegex(ValueError, "future-version corpus"):
                fixture_files.check_inventory(root, "1.0.2-rc.10")
            fixture_files.check_inventory(root, "1.0.2")

    def test_versioned_links_are_refused(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "1.0.1").symlink_to(root, target_is_directory=True)
            with self.assertRaisesRegex(ValueError, "regular directory"):
                fixture_files.check_inventory(root, "1.0.2")

    def test_inventory_cannot_validate_a_different_fixture(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            with self.assertRaisesRegex(ValueError, "outside the selected"):
                fixture_files.candidate(root / "elsewhere", Path("engine"), "1.0.2", root)


BASELINE_SPEC = importlib.util.spec_from_file_location(
    "release_compatibility_baseline", Path(__file__).resolve().parents[2].joinpath("release-compatibility-baseline.py"))
release_baseline = importlib.util.module_from_spec(BASELINE_SPEC)
BASELINE_SPEC.loader.exec_module(release_baseline)


class PublishedStableBaselineTests(unittest.TestCase):
    release = {"draft": False, "prerelease": False, "tag_name": "v1.0.1", "published_at": "2026-10-02T19:44:48Z"}

    def git(self, command, **_):
        if command[1:3] == ["rev-parse", "--verify"]:
            return "a" * 40
        if command[1:] == ["show", "a" * 40 + ":VERSION"]:
            return "1.0.1\n"
        if command[1] == "show":
            return self.candidate
        return "b" * 40

    def test_published_stable_is_pinned_for_both_candidate_channels(self):
        for candidate in ("1.0.2-rc3", "1.0.2"):
            with self.subTest(candidate=candidate):
                self.candidate = candidate
                with patch.object(release_baseline.subprocess, "check_output", side_effect=self.git), \
                     patch.object(release_baseline.subprocess, "run") as ancestry:
                    self.assertEqual(release_baseline.resolve(self.release, "selected-source"), ("v1.0.1", "a" * 40))
                    ancestry.assert_called_once_with(["git", "merge-base", "--is-ancestor", "a" * 40, "selected-source"], check=True)

    def test_drafts_previews_and_unpublished_tags_cannot_reset_the_baseline(self):
        for changes in ({"draft": True}, {"prerelease": True}, {"published_at": None},
                        {"tag_name": "v1.0.2-rc3"}, {"tag_name": "v0.9.0"}, {"tag_name": "main"}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                release_baseline.resolve({**self.release, **changes})

    def test_product_downgrades_and_reused_release_versions_fail(self):
        for candidate in ("1.0.0", "1.0.1"):
            self.candidate = candidate
            with self.subTest(candidate=candidate), \
                 patch.object(release_baseline.subprocess, "check_output", side_effect=self.git), \
                 patch.object(release_baseline.subprocess, "run"), self.assertRaises(ValueError):
                release_baseline.resolve(self.release)

    def test_tag_source_mismatch_fails(self):
        with patch.object(release_baseline.subprocess, "check_output", side_effect=["a" * 40, "1.0.0"]), \
             self.assertRaisesRegex(ValueError, "does not match"):
            release_baseline.resolve(self.release)

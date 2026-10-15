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
        manifest = {"app_version": self.metadata["app_version"], "format_version": 1,
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

    def test_every_published_corpus_retains_its_schema_and_exact_release_bytes(self):
        target = self.root / "lycaon/testdata/upgrade-corpus/1.0.1"
        target.parent.mkdir(parents=True)
        self.fixture.rename(target)
        self.fixture = target
        self.metadata["app_version"] = "1.0.1"
        self.write_archive()
        baseline = {p.relative_to(self.root).as_posix(): p.read_bytes()
                    for p in self.fixture.rglob("*") if p.is_file()}
        def git(command):
            if command[3] == "ls-tree":
                return ("\0".join(baseline) + "\0").encode()
            return baseline[command[4].split(":", 1)[1]]
        with patch.object(fixture_files.subprocess, "check_output", side_effect=git):
            fixture_files.released_corpus(target.parent, ["1.0.1"], self.root, [self.identity])
            with self.assertRaisesRegex(ValueError, "retained baseline"):
                fixture_files.released_corpus(target.parent, ["1.0.1"], self.root, [])
            with self.assertRaisesRegex(ValueError, "missing"):
                fixture_files.released_corpus(target.parent, ["1.0.2"])
            (self.fixture / "SEMANTICS.json").write_text('{"rewritten":true}')
            self.write_archive()
            with self.assertRaisesRegex(ValueError, "bytes differ"):
                fixture_files.released_corpus(target.parent, ["1.0.1"], self.root, [self.identity])

    def test_static_gate_needs_no_engine_and_rejects_stale_schema_revision(self):
        lock = self.root / "schema-lock.json"
        lock.write_text('{"schema_version":1}')
        with patch.object(fixture_files.subprocess, "check_output", side_effect=AssertionError("engine invoked")):
            fixture_files.candidate_static(self.fixture, "1.0.0-rc.1", schema_lock=lock)
            lock.write_text('{"schema_version":2}')
            with self.assertRaisesRegex(ValueError, "revision differs"):
                fixture_files.candidate_static(self.fixture, "1.0.0-rc.1", schema_lock=lock)

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
        for changes in ({"draft": True}, {"prerelease": True}, {"published_at": None}, {"published_at": "yesterday"},
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


class StableFeedBaselineTests(unittest.TestCase):
    mirror = PublishedStableBaselineTests.release

    def setUp(self):
        registry = release_baseline.load_registry()
        row = {**registry["generations"][0], "generation": 1, "successor": None, "bridge_version": None}
        self.registry = {"schema_version": 1, "signing_generation": 1, "embedded_generation": 1, "generations": [row]}
        self.patch_registry = patch.object(release_baseline, "load_registry", return_value=self.registry)
        self.patch_registry.start()
        self.addCleanup(self.patch_registry.stop)

    def manifest(self, version):
        from update_keys import release_binding
        return json.dumps({"version": version, "pub_date": "2026-10-03T19:41:04Z",
                           "update_keys": release_binding(self.registry, version)}).encode()

    def test_signed_feed_can_advance_before_the_github_mirror(self):
        raw = self.manifest("1.0.2")
        with patch.object(release_baseline, "read_public", side_effect=[raw, b"signature"]), \
             patch.object(release_baseline.feed_signature, "check") as binding, \
             patch.object(release_baseline.feed_signing, "verify") as crypto:
            selected = release_baseline.published_stable(self.mirror, "https://downloads.example")
            self.assertEqual(selected["tag_name"], "v1.0.2")
            self.assertEqual(selected["feed_manifest_sha256"], sha(raw))
            binding.assert_called_once()
            crypto.assert_called_once()

    def test_a_newer_published_mirror_is_not_lost_to_an_older_feed(self):
        with patch.object(release_baseline, "read_public", side_effect=[self.manifest("1.0.0"), b"signature"]), \
             patch.object(release_baseline.feed_signature, "check"), patch.object(release_baseline.feed_signing, "verify"):
            self.assertEqual(release_baseline.published_stable(self.mirror, "https://downloads.example"), self.mirror)

    def test_missing_or_unverifiable_feed_blocks_baseline_selection(self):
        for replies, verify_error in (([None], None), ([self.manifest("1.0.1"), None], None),
                                      ([self.manifest("1.0.1"), b"signature"], ValueError("invalid signature"))):
            with self.subTest(replies=replies), \
                 patch.object(release_baseline, "read_public", side_effect=replies), \
                 patch.object(release_baseline.feed_signature, "check"), \
                 patch.object(release_baseline.feed_signing, "verify", side_effect=verify_error), self.assertRaises(ValueError):
                release_baseline.published_stable(self.mirror, "https://downloads.example")

    def test_preview_feed_values_and_unsafe_origins_are_refused(self):
        with patch.object(release_baseline, "read_public", side_effect=[self.manifest("1.0.2-rc3"), b"signature"]), \
             self.assertRaisesRegex(ValueError, "stable"):
            release_baseline.published_stable(self.mirror, "https://downloads.example")
        for origin in ("http://downloads.example", "https://downloads.example/path", "https://user@downloads.example"):
            with self.subTest(origin=origin), self.assertRaisesRegex(ValueError, "HTTPS download origin"):
                release_baseline.published_stable(self.mirror, origin)

    def test_public_reads_are_bounded_and_only_404_means_absent(self):
        from io import BytesIO
        with patch.object(release_baseline.urllib.request, "urlopen", return_value=BytesIO(b"{}")):
            self.assertEqual(release_baseline.read_public("https://downloads.example"), b"{}")
        with patch.object(release_baseline.urllib.request, "urlopen", return_value=BytesIO(b"x" * (2 * 1024 * 1024 + 1))), \
             self.assertRaisesRegex(ValueError, "size bound"):
            release_baseline.read_public("https://downloads.example")
        for status in (404, 403):
            error = release_baseline.urllib.error.HTTPError("https://downloads.example", status, "fixture", None, None)
            with patch.object(release_baseline.urllib.request, "urlopen", side_effect=error):
                if status == 404:
                    self.assertIsNone(release_baseline.read_public("https://downloads.example"))
                else:
                    with self.assertRaises(release_baseline.urllib.error.HTTPError):
                        release_baseline.read_public("https://downloads.example")


class ReleaseProgressionTests(unittest.TestCase):
    def release(self, version):
        return {**PublishedStableBaselineTests.release, "tag_name": "v" + version,
                "prerelease": "-" in version}

    def test_each_channel_must_advance_with_semver_precedence(self):
        for candidate, previous, valid in (("1.0.2-rc10", "1.0.2-rc9", False),
                                           ("1.0.2-rc.10", "1.0.2-rc.9", True),
                                           ("1.0.2-rc3", "1.0.2-rc3", False),
                                           ("1.0.2", "1.0.1", True),
                                           ("1.0.1", "1.0.2", False)):
            with self.subTest(candidate=candidate, previous=previous), \
                 patch.object(release_baseline.subprocess, "check_output", return_value="20"):
                if valid:
                    release_baseline.check_progression(candidate, [], self.release(previous), "source")
                else:
                    with self.assertRaisesRegex(ValueError, "newer"):
                        release_baseline.check_progression(candidate, [], self.release(previous), "source")

    def test_native_build_advances_across_both_channels(self):
        releases = [self.release("1.0.1"), self.release("1.0.2-rc3")]
        for builds, valid in ((["22", "19", "21"], True), (["21", "19", "21"], False)):
            with self.subTest(builds=builds), patch.object(release_baseline.subprocess, "check_output", side_effect=builds):
                if valid:
                    release_baseline.check_progression("1.0.2", releases, releases[0], "source")
                else:
                    with self.assertRaisesRegex(ValueError, "RELEASE_BUILD"):
                        release_baseline.check_progression("1.0.2", releases, releases[0], "source")

    def test_preview_history_does_not_replace_stable_history(self):
        releases = [self.release("1.0.1"), self.release("1.1.0-rc.2"),
                    {**self.release("2.0.0"), "draft": True}, self.release("1.1.0-rc.10")]
        self.assertEqual(release_baseline.channel_mirror(releases, "stable")["tag_name"], "v1.0.1")
        self.assertEqual(release_baseline.channel_mirror(releases, "preview")["tag_name"], "v1.1.0-rc.10")

    def test_build_metadata_cannot_republish_a_version(self):
        with self.assertRaisesRegex(ValueError, "without build metadata"):
            release_baseline.check_progression("1.0.2+another", [], None, "source")


class SourceVersionTests(unittest.TestCase):
    def inputs(self):
        return {"VERSION": "1.0.2-rc3", "RELEASE_BUILD": "20",
                "lycaon-den/package.json": '{"version":"1.0.2-rc3"}',
                "lycaon-den/src-tauri/tauri.conf.json": '{"version":"1.0.2","bundle":{"macOS":{"bundleVersion":"20"}}}',
                "lycaon-den/src-tauri/Cargo.toml": '[package]\nversion="1.0.2"',
                "lycaon-den/src-tauri/Cargo.lock": '[[package]]\nname="painted-wolf-code"\nversion="1.0.2"',
                "CHANGELOG.md": '## [1.0.2-rc3]\nRelease notes.\n## [1.0.1]\nPrior notes.'}

    def test_selected_source_packaging_versions_and_notes_are_checked_before_building(self):
        source = self.inputs()
        def read(command, **_):
            self.assertEqual(command[:2], ["git", "show"])
            self.assertTrue(command[2].startswith("selected:"))
            return source[command[2].split(":", 1)[1]]
        with patch.object(release_baseline.subprocess, "check_output", side_effect=read):
            self.assertEqual(release_baseline.check_source_versions("selected"), "1.0.2-rc3")
            for name, replacement in (("lycaon-den/package.json", '{"version":"1.0.1"}'),
                                      ("lycaon-den/src-tauri/Cargo.toml", '[package]\nversion="1.0.1"'),
                                      ("lycaon-den/src-tauri/Cargo.lock", '[[package]]\nname="painted-wolf-code"\nversion="1.0.1"'),
                                      ("RELEASE_BUILD", "19"), ("CHANGELOG.md", '## [1.0.1]\nPrior notes.'),
                                      ("CHANGELOG.md", '## [1.0.2-rc3] - 2026-10-15\n\n## [1.0.1]\nPrior notes.')):
                original = source[name]
                source[name] = replacement
                with self.subTest(name=name), self.assertRaises(ValueError):
                    release_baseline.check_source_versions("selected")
                source[name] = original


class SigningHistoryTests(unittest.TestCase):
    def registry(self):
        row = {**release_baseline.load_registry()["generations"][0], "generation": 1,
               "successor": None, "bridge_version": None}
        return {"schema_version": 1, "signing_generation": 1, "embedded_generation": 1, "generations": [row]}

    def test_published_source_keys_are_checked_without_building_old_releases(self):
        current = self.registry()
        previous = json.loads(json.dumps(current))
        del previous["generations"][0]["feed_public_key"]
        with patch.object(release_baseline.subprocess, "check_output", side_effect=[json.dumps(current), json.dumps(previous)]) as git:
            release_baseline.check_published_keys("selected", [PublishedStableBaselineTests.release])
            self.assertEqual([call.args[0][2] for call in git.call_args_list],
                             ["selected:packaging/update-keys.json", "refs/tags/v1.0.1:packaging/update-keys.json"])

    def test_published_keys_and_closed_bridges_are_permanent(self):
        import copy
        current = self.registry()
        old = copy.deepcopy(current)
        release_baseline.check_key_history(old, current)
        # Early releases carried the artifact key before feed signing was added.
        del old["generations"][0]["feed_public_key"]
        release_baseline.check_key_history(old, current)
        old = copy.deepcopy(current)
        old["generations"][0]["public_key"] = current["generations"][0]["feed_public_key"]
        with self.assertRaisesRegex(ValueError, "signing key"):
            release_baseline.check_key_history(old, current)
        old = copy.deepcopy(current)
        old["embedded_generation"] = current["embedded_generation"] + 1
        with self.assertRaisesRegex(ValueError, "regressed"):
            release_baseline.check_key_history(old, current)
        old = copy.deepcopy(current)
        old["generations"][0].update(successor=2, bridge_version="1.0.2")
        with self.assertRaisesRegex(ValueError, "bridge changed"):
            release_baseline.check_key_history(old, current)


class ActivatedHistoryTests(unittest.TestCase):
    def test_activation_survives_a_missing_github_mirror_and_later_pointer(self):
        from release_distribution import encoded
        from update_keys import release_binding
        registry = release_baseline.load_registry()
        version = "1.0.2-rc3"
        manifest = {"version": version, "pub_date": "2026-10-03T12:00:00Z",
                    "update_keys": release_binding(registry, version)}
        activation = {"version": version, "manifest_sha256": sha(encoded(manifest)),
                      "started_at": "2026-10-03T12:05:00Z"}
        def read(url):
            if "/1.0.2-rc3/activation.json" in url:
                return json.dumps(activation).encode()
            if url.endswith("/updates/releases/1.0.2-rc3.json"):
                return encoded(manifest)
            return None
        with patch.object(release_baseline.subprocess, "check_output", return_value="v1.0.1\nv1.0.2-rc3\nv1.0.2\nv0.9.0\nvbad\n"), \
             patch.object(release_baseline, "read_public", side_effect=read):
            history = release_baseline.activated_history([PublishedStableBaselineTests.release], "https://downloads.example")
            self.assertEqual([row["tag_name"] for row in history], ["v1.0.2-rc3"])
            self.assertEqual(history[0]["published_at"], activation["started_at"])
            activation["manifest_sha256"] = "0" * 64
            with self.assertRaisesRegex(ValueError, "immutable release"):
                release_baseline.activated_history([], "https://downloads.example")

    def test_unverifiable_activation_cannot_be_ignored_as_an_unpublished_tag(self):
        with patch.object(release_baseline.subprocess, "check_output", return_value="v1.0.2\n"), \
             patch.object(release_baseline, "read_public", side_effect=[b"{}", None]), \
             self.assertRaisesRegex(ValueError, "no immutable manifest"):
            release_baseline.activated_history([], "https://downloads.example")

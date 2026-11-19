"""Release trust history: retained clients stay covered after pointers advance, artifact signing
follows the trust generation, and historical sources keep their published platform set."""
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from release_upgrade.plan import plan
from update_keys import bundler_config, load_registry, release_binding
from updater_signature import rehearsal_signature
from verification_tests.distribution.test_upgrade_plan_execution import manifest, snapshot

ROOT = Path(__file__).resolve().parents[3]
ORIGIN = "https://downloads.paintedwolf.dev"


def anchor(value):
    return {"path": "anchors/1.0.2.json", "manifest": value,
            "verification": "artifact_signature_required"}


class AnchorPlanTests(unittest.TestCase):
    def setUp(self):
        binding = patch("release_upgrade.plan.validate_binding")
        binding.start()
        self.addCleanup(binding.stop)

    def test_first_automatic_client_survives_both_channel_pointers_advancing(self):
        baseline = manifest("1.0.2")
        result = plan(manifest("1.3.0"), {},
                      [snapshot("stable", manifest("1.2.0")), snapshot("preview", manifest("1.3.0-rc1"))],
                      [anchor(baseline)])
        cases = [case for case in result["cases"] if case["source_version"] == "1.0.2"]
        self.assertEqual([case["channel"] for case in cases], ["preview", "stable"])
        for case in cases:
            self.assertEqual(case["source_kind"], "automatic_baseline")
            self.assertEqual(case["source_manifest"], baseline)
            self.assertEqual(case["source_path"], "anchors/1.0.2.json")
            self.assertEqual(case["initial_generation"], 1)
        self.assertEqual(len(result["anchors"]), 1)

    def test_authenticated_live_provenance_wins_when_the_baseline_is_still_active(self):
        source = manifest("1.0.2")
        result = plan(manifest("1.1.0"), {}, [snapshot("stable", source)], [anchor(source)])
        self.assertEqual(len(result["cases"]), 2)
        self.assertTrue(all(case["source_kind"] == "feed" for case in result["cases"]))
        self.assertTrue(all(case["source_path"] == "updates/stable/key-1/latest.json" for case in result["cases"]))

    def test_a_preview_pointer_cannot_remove_stable_baseline_coverage(self):
        source = manifest("1.0.2")
        result = plan(manifest("1.1.0"), {}, [snapshot("preview", source)], [anchor(source)])
        cases = {case["channel"]: case for case in result["cases"]}
        self.assertEqual(cases["stable"]["source_kind"], "automatic_baseline")
        self.assertEqual(cases["preview"]["source_kind"], "feed")

    def test_the_baseline_preserves_a_preview_offer_newer_than_the_stable_candidate(self):
        result = plan(manifest("1.1.0"), {}, [snapshot("preview", manifest("1.2.0-rc1"))],
                      [anchor(manifest("1.0.2"))])
        self.assertEqual([case["channel"] for case in result["cases"]], ["stable"])

    def test_baseline_crosses_every_retained_trust_generation(self):
        feeds = [snapshot("preview", manifest("1.1.0", 1, 2)),
                 snapshot("preview", manifest("1.2.0", 2, 3), 2),
                 snapshot("stable", manifest("1.3.0", 3), 3)]
        bridges = {1: {"bridge_version": "1.1.0"}, 2: {"bridge_version": "1.2.0"}}
        with patch("release_upgrade.plan.generation", side_effect=lambda _, number: bridges[number]):
            result = plan(manifest("1.4.0-rc1", 3), {}, feeds, [anchor(manifest("1.0.2"))])
        case = next(case for case in result["cases"] if case["source_version"] == "1.0.2")
        self.assertEqual([step["version"] for step in case["steps"]], ["1.1.0", "1.2.0", "1.4.0-rc1"])
        self.assertEqual([step["update_keys"]["signing_generation"] for step in case["steps"]], [1, 2, 3])
        installed_bridge = next(case for case in result["cases"] if case["source_version"] == "1.1.0")
        self.assertEqual(installed_bridge["source_signing_generation"], 1)
        self.assertEqual(installed_bridge["initial_generation"], 2)
        self.assertEqual(installed_bridge["steps"][0]["version"], "1.2.0")

    def test_deduplication_cannot_hide_conflicting_artifacts_for_one_release(self):
        original = {**manifest("1.0.2"), "platforms": {"darwin-aarch64": {"url": "first"}}}
        changed = {**manifest("1.0.2"), "platforms": {"darwin-aarch64": {"url": "second"}}}
        with self.assertRaisesRegex(ValueError, "conflicting artifact or trust"):
            plan(manifest("1.1.0-rc1"), {}, [snapshot("preview", changed)], [anchor(original)])

    def test_public_source_capture_includes_the_retained_baseline_in_its_saved_plan(self):
        import json
        from pathlib import Path
        import tempfile
        from release_control import capture_sources
        candidate = manifest("1.1.0-rc1")
        with tempfile.TemporaryDirectory() as temporary, \
             patch("update_keys.load_registry", return_value={"generations": [{"generation": 1}]}), \
             patch("release_upgrade.feeds.capture", return_value=None), \
             patch("release_upgrade.baseline.capture", return_value=[anchor(manifest("1.0.2"))]):
            result = capture_sources("https://downloads.paintedwolf.dev", candidate, Path(temporary))
            saved = json.loads((Path(temporary) / "plan.json").read_text())
        self.assertEqual(saved, result)
        self.assertEqual(result["cases"][0]["source_version"], "1.0.2")
        self.assertEqual(result["cases"][0]["channel"], "preview")


class BundleConfigTests(unittest.TestCase):
    def test_ordinary_and_bridge_releases_select_the_artifact_signing_key(self):
        for embedded in (1, 2):
            registry = {"signing_generation": 1, "embedded_generation": embedded,
                        "generations": [{"public_key": "artifact-one", "feed_public_key": "feed-one"},
                                        {"public_key": "artifact-two", "feed_public_key": "feed-two"}]}
            self.assertEqual(bundler_config(registry),
                             {"plugins": {"updater": {"pubkey": "artifact-one"}}})

    def test_rotated_release_selects_its_new_artifact_key(self):
        registry = {"signing_generation": 2, "generations": [{"public_key": "old"}, {"public_key": "new"}]}
        self.assertEqual(bundler_config(registry)["plugins"]["updater"]["pubkey"], "new")


class ManifestHistoryTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        (self.root / "scripts").mkdir()
        (self.root / "packaging").mkdir()
        for name in ("release-validate-updater-manifest.sh", "semver-compare.py", "release_semver.py", "update_keys.py", "updater_signature.py"):
            shutil.copyfile(ROOT / "scripts" / name, self.root / "scripts" / name)
        shutil.copytree(ROOT / "scripts/release_distribution_contract", self.root / "scripts/release_distribution_contract")
        shutil.copyfile(ROOT / "packaging/update-keys.json", self.root / "packaging/update-keys.json")
        catalog = json.loads((ROOT / "packaging/release-platforms.json").read_text())
        for row in catalog["platforms"]:
            if row["updater_key"] == "windows-x86_64":
                row["publication"] = "public"
        (self.root / "packaging/release-platforms.json").write_text(json.dumps(catalog))
        keys = load_registry()
        keys["signing_generation"] = keys["embedded_generation"] = len(keys["generations"])
        version = "1.0.2"
        self.manifest = {"version": version, "notes": "Retained source", "pub_date": "2026-01-01T00:00:00Z",
                         "update_keys": release_binding(keys, version),
                         "platforms": {"darwin-aarch64": {"signature": rehearsal_signature(version),
                             "url": ORIGIN + f"/releases/v{version}/painted-wolf-code_v{version}_darwin-aarch64.app.tar.gz"}}}

    def validate(self, existing):
        import os
        path = self.root / "manifest.json"
        path.write_text(json.dumps(self.manifest))
        arguments = ["bash", str(self.root / "scripts/release-validate-updater-manifest.sh"), "--file", str(path)]
        if existing:
            arguments.append("--existing")
        return subprocess.run(arguments, env={**os.environ, "DOWNLOAD_BASE_URL": ORIGIN}, capture_output=True, text=True)

    def test_published_sources_keep_their_original_platforms_but_new_offers_require_every_public_platform(self):
        self.assertEqual(self.validate(True).returncode, 0)
        self.assertNotEqual(self.validate(False).returncode, 0)

    def test_empty_and_unknown_historical_platform_sets_are_refused(self):
        self.manifest["platforms"] = {}
        self.assertNotEqual(self.validate(True).returncode, 0)
        self.manifest["platforms"] = {"unknown-aarch64": {"signature": "signature", "url": ORIGIN}}
        self.assertNotEqual(self.validate(True).returncode, 0)

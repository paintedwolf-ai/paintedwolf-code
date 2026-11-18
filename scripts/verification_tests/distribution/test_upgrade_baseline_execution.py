"""The first stable automatic updater remains reachable across skipped releases."""
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import release_upgrade.baseline as baseline


class BaselineTests(unittest.TestCase):
    def test_initial_candidates_do_not_require_an_unpublished_baseline(self):
        for candidate in ("1.0.2-rc8", "1.0.2"):
            with self.subTest(candidate=candidate), patch.object(baseline, "read_bytes") as read:
                self.assertEqual(baseline.capture("https://downloads.paintedwolf.dev", candidate, {}, [], Path("unused")), [])
                read.assert_not_called()

    def test_an_active_baseline_feed_supplies_the_existing_authenticated_source(self):
        with patch.object(baseline, "read_bytes") as read:
            sources = [{"path": "updates/preview/key-1/latest.json", "manifest_sha256": "digest", "manifest": {"version": "1.0.2"}}]
            result = baseline.capture("https://downloads.paintedwolf.dev", "1.1.0", {}, sources, Path("unused"))
            self.assertEqual(result[0]["manifest"], sources[0]["manifest"])
            self.assertEqual(result[0]["path"], sources[0]["path"])
            self.assertEqual(result[0]["verification"], "artifact_signature_required")
            read.assert_not_called()

    def test_archived_metadata_requires_artifact_authentication_before_execution(self):
        raw = b'{"version":"1.0.2","update_keys":{}}'
        with tempfile.TemporaryDirectory() as temporary, \
             patch.object(baseline, "read_bytes", return_value=raw) as read, \
             patch.object(baseline, "validate_binding"), \
             patch.object(baseline.subprocess, "run") as validate:
            result = baseline.capture("https://downloads.paintedwolf.dev", "1.1.0", {}, [], Path(temporary))
            self.assertEqual(read.call_args.args[0], "https://downloads.paintedwolf.dev/updates/releases/1.0.2.json")
            self.assertEqual((Path(temporary) / result[0]["path"]).read_bytes(), raw)
            self.assertEqual(result[0]["verification"], "artifact_signature_required")
            self.assertTrue(Path(validate.call_args.args[0][1]).is_file())

    def test_missing_or_misdirected_baselines_fail_closed(self):
        for raw in (None, b'{"version":"1.0.3"}'):
            with self.subTest(raw=raw), patch.object(baseline, "read_bytes", return_value=raw):
                with self.assertRaises(ValueError):
                    baseline.capture("https://downloads.paintedwolf.dev", "1.1.0", {}, [], Path("unused"))


class PublishedSourceTests(unittest.TestCase):
    """The nightly installed upgrade runs from the release before the newest to the newest."""

    def setUp(self):
        self.root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        for version in ("1.0.1", "1.0.2-rc7", "1.0.2-rc8", "1.0.2", "1.0.3-rc1"):
            (self.root / "lycaon/testdata/upgrade-corpus" / version).mkdir(parents=True)
        self.registry = {"signing_generation": 1, "generations": [{"generation": 1, "bridge_version": None}]}
        self.pointers = {"stable": "1.0.2", "preview": "1.0.2"}
        self.published = {"1.0.1", "1.0.2-rc7", "1.0.2-rc8", "1.0.2"}

    def capture(self):
        import release_control
        def pointer(_base, channel, number, _registry, _destination):
            version = self.pointers.get(channel)
            if version is None:
                return None
            return {"channel": channel, "generation": number, "path": f"updates/{channel}/key-{number}/latest.json",
                    "manifest": {"version": version, "update_keys": {"signing_generation": 1, "embedded_generation": 1}}}
        def retained(_base, version, _registry, _destination):
            if version not in self.published:
                return None
            return {"path": f"anchors/{version}.json", "verification": "artifact_signature_required",
                    "manifest": {"version": version, "update_keys": {"signing_generation": 1, "embedded_generation": 1}}}
        with patch("update_keys.load_registry", return_value=self.registry), \
             patch("release_upgrade.feeds.capture", side_effect=pointer), \
             patch.object(baseline, "retained", side_effect=retained) as reads, \
             patch("release_upgrade.plan.validate_binding"):
            result = release_control.capture_published("https://downloads.paintedwolf.dev", self.root, self.root / "sources")
        return result, [call.args[1] for call in reads.call_args_list]

    def test_the_newest_pointer_is_the_candidate_and_the_newest_earlier_corpus_the_source(self):
        result, reads = self.capture()
        self.assertEqual(result["candidate"]["version"], "1.0.2")
        self.assertEqual(result["candidate_source"], "published")
        self.assertEqual(reads, ["1.0.2-rc8"])
        self.assertEqual(sorted(case["id"] for case in result["cases"]), ["1.0.2-rc8-preview", "1.0.2-rc8-stable"])

    def test_a_sealed_but_unpublished_corpus_is_passed_over(self):
        self.pointers["preview"] = "1.0.3-rc2"
        (self.root / "lycaon/testdata/upgrade-corpus/1.0.3-rc2").mkdir()
        result, reads = self.capture()
        self.assertEqual(result["candidate"]["version"], "1.0.3-rc2")
        self.assertEqual(reads, ["1.0.3-rc1", "1.0.2"])
        self.assertEqual([case["id"] for case in result["cases"]], ["1.0.2-preview"])

    def test_manual_install_clients_and_an_empty_origin_are_refused(self):
        self.published = {"1.0.1"}
        with self.assertRaisesRegex(ValueError, "no earlier published release"):
            self.capture()
        self.pointers = {}
        with self.assertRaisesRegex(ValueError, "no release is published"):
            self.capture()

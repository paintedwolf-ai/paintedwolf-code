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
            sources = [{"manifest": {"version": "1.0.2"}}]
            self.assertEqual(baseline.capture("https://downloads.paintedwolf.dev", "1.1.0", {}, sources, Path("unused")), [])
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

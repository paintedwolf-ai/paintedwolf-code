"""Upgrade paths preserve channel and advance through authenticated bridges."""
import unittest
from unittest.mock import patch

from release_upgrade_plan import plan


def manifest(version, signing=1, embedded=None):
    return {"version": version, "update_keys": {"signing_generation": signing,
            "embedded_generation": embedded or signing}}


def snapshot(channel, value, number=1):
    return {"channel": channel, "generation": number,
            "path": f"updates/{channel}/key-{number}/latest.json", "manifest": value}


class UpgradePlanTests(unittest.TestCase):
    def setUp(self):
        self.binding = patch("release_upgrade_plan.validate_binding")
        self.binding.start()
        self.addCleanup(self.binding.stop)
        self.registry = {"generations": [{"bridge_version": "1.1.0"}]}

    def test_manual_clients_are_explicitly_excluded_from_desktop_coverage(self):
        result = plan(manifest("1.1.0"), self.registry, [snapshot("stable", manifest("1.0.1"))])
        self.assertEqual(result["cases"], [])
        self.assertEqual(result["excluded_sources"][0]["reason"], "requires_manual_installation")

    def test_shipped_protocol_boundary_is_explicit(self):
        from release_upgrade_plan import client_protocol
        self.assertEqual(client_protocol("1.0.1"), "tauri-plugin-v1")
        self.assertEqual(client_protocol("1.0.2-rc1"), "native-v2")

    def test_stable_promotes_preview_without_changing_the_preference(self):
        result = plan(manifest("1.2.0"), self.registry,
                      [snapshot("stable", manifest("1.0.2")), snapshot("preview", manifest("1.2.0-rc.1"))])
        self.assertEqual([(case["source_version"], case["channel"]) for case in result["cases"]],
                         [("1.0.2", "preview"), ("1.0.2", "stable"), ("1.2.0-rc.1", "preview")])
        self.assertIsNone(result["skip_reason"])

    def test_preview_covers_opt_in_from_stable_and_the_prior_preview(self):
        result = plan(manifest("1.2.0-rc.2"), self.registry,
                      [snapshot("stable", manifest("1.0.2")), snapshot("preview", manifest("1.2.0-rc.1"))])
        self.assertEqual([case["channel"] for case in result["cases"]], ["preview", "preview"])

    def test_missing_preview_uses_stable_and_equal_or_newer_offers_are_excluded(self):
        result = plan(manifest("1.1.0-rc.1"), self.registry, [snapshot("stable", manifest("1.0.2"))])
        self.assertEqual(len(result["cases"]), 1)
        result = plan(manifest("1.0.2"), self.registry,
                      [snapshot("stable", manifest("1.0.2")), snapshot("preview", manifest("1.1.0-rc.1"))])
        self.assertEqual(result["cases"], [])
        self.assertEqual(result["skip_reason"], "no_automatic_upgrade_source")

    def test_withdrawn_sources_remain_in_the_matrix(self):
        source = {**manifest("1.0.2"), "withdrawn": True}
        self.assertEqual(len(plan(manifest("1.1.0"), self.registry, [snapshot("stable", source)])["cases"]), 2)

    def test_skipped_generations_follow_each_channel_bridge(self):
        feeds = [snapshot("stable", manifest("1.0.2")),
                 snapshot("preview", manifest("1.1.0", 1, 2)),
                 snapshot("stable", manifest("1.2.0", 2), 2)]
        with patch("release_upgrade_plan.generation", return_value={"bridge_version": "1.1.0"}):
            result = plan(manifest("1.3.0-rc.1", 2), self.registry, feeds)
        source = next(case for case in result["cases"] if case["source_version"] == "1.0.2")
        self.assertEqual([step["version"] for step in source["steps"]], ["1.1.0", "1.3.0-rc.1"])

    def test_missing_or_withdrawn_bridge_refuses_the_plan(self):
        for bridge in (None, {**manifest("1.1.0", 1, 2), "withdrawn": True}):
            feeds = [snapshot("stable", manifest("1.0.2"))]
            if bridge:
                feeds.append(snapshot("preview", bridge))
            with patch("release_upgrade_plan.generation", return_value={"bridge_version": "1.1.0"}):
                with self.assertRaises(ValueError):
                    plan(manifest("1.3.0-rc.1", 2), self.registry, feeds)

    def test_invalid_stable_and_duplicate_feeds_are_refused(self):
        bad = snapshot("stable", manifest("1.0.1-rc.1"))
        for feeds in ([bad], [snapshot("stable", manifest("1.0.2"))] * 2):
            with self.assertRaises(ValueError):
                plan(manifest("1.1.0"), self.registry, feeds)


class FeedCaptureTests(unittest.TestCase):
    def test_capture_authenticates_original_bytes_and_channel_binding(self):
        import hashlib
        from pathlib import Path
        import tempfile
        import release_upgrade_feeds as feeds
        raw = b'{"version":"1.0.1"}'
        with tempfile.TemporaryDirectory() as temporary, \
             patch.object(feeds, "read_bytes", side_effect=[raw, b"signed"]), \
             patch.object(feeds, "validate_binding"), \
             patch.object(feeds, "generation", return_value={"feed_public_key": "public"}), \
             patch.object(feeds.feed_signature, "check") as binding, \
             patch.object(feeds.feed_signing, "verify") as verify, \
             patch.object(feeds.subprocess, "run"):
            result = feeds.capture(feeds.ORIGIN, "preview", 1, {}, Path(temporary))
            binding.assert_called_once_with("signed", file="latest-preview-key-1.json", version="1.0.1", number=1, registry={})
            pointer = verify.call_args.args[0]
            self.assertEqual(pointer.read_bytes(), raw)
            self.assertEqual(result["manifest_sha256"], hashlib.sha256(raw).hexdigest())
            self.assertEqual((Path(temporary) / (result["path"] + ".sig")).read_bytes(), b"signed")

    def test_missing_signature_and_invalid_signature_fail_closed(self):
        from pathlib import Path
        import tempfile
        import release_upgrade_feeds as feeds
        for signature in (None, b"invalid"):
            with self.subTest(signature=signature), tempfile.TemporaryDirectory() as temporary, \
                 patch.object(feeds, "read_bytes", side_effect=[b'{"version":"1.0.1"}', signature]), \
                 patch.object(feeds, "validate_binding"), \
                 patch.object(feeds.feed_signature, "check", side_effect=ValueError("signature rejected")):
                with self.assertRaises(ValueError):
                    feeds.capture(feeds.ORIGIN, "stable", 1, {}, Path(temporary))

    def test_missing_pointer_is_the_only_optional_network_outcome(self):
        from pathlib import Path
        import release_upgrade_feeds as feeds
        with patch.object(feeds, "read_bytes", return_value=None):
            self.assertIsNone(feeds.capture(feeds.ORIGIN, "stable", 1, {}, Path("unused")))
        with patch.object(feeds, "read_bytes", side_effect=TimeoutError("origin unavailable")):
            with self.assertRaises(TimeoutError):
                feeds.capture(feeds.ORIGIN, "stable", 1, {}, Path("unused"))

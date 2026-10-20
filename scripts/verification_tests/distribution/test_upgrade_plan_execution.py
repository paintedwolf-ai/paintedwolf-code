"""Upgrade paths preserve channel and advance through authenticated bridges."""
import copy
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

    def test_stable_promotes_preview_without_changing_the_preference(self):
        result = plan(manifest("1.2.0"), self.registry,
                      [snapshot("stable", manifest("1.0.0")), snapshot("preview", manifest("1.2.0-rc.1"))])
        self.assertEqual([(case["source_version"], case["channel"]) for case in result["cases"]],
                         [("1.0.0", "preview"), ("1.0.0", "stable"), ("1.2.0-rc.1", "preview")])
        self.assertIsNone(result["skip_reason"])

    def test_preview_covers_opt_in_from_stable_and_the_prior_preview(self):
        result = plan(manifest("1.2.0-rc.2"), self.registry,
                      [snapshot("stable", manifest("1.0.0")), snapshot("preview", manifest("1.2.0-rc.1"))])
        self.assertEqual([case["channel"] for case in result["cases"]], ["preview", "preview"])

    def test_missing_preview_uses_stable_and_equal_or_newer_offers_are_excluded(self):
        result = plan(manifest("1.1.0-rc.1"), self.registry, [snapshot("stable", manifest("1.0.0"))])
        self.assertEqual(len(result["cases"]), 1)
        result = plan(manifest("1.0.0"), self.registry,
                      [snapshot("stable", manifest("1.0.0")), snapshot("preview", manifest("1.1.0-rc.1"))])
        self.assertEqual(result["cases"], [])
        self.assertEqual(result["skip_reason"], "no_older_published_source")

    def test_withdrawn_sources_remain_in_the_matrix(self):
        source = {**manifest("1.0.0"), "withdrawn": True}
        self.assertEqual(len(plan(manifest("1.1.0"), self.registry, [snapshot("stable", source)])["cases"]), 2)

    def test_skipped_generations_follow_each_channel_bridge(self):
        feeds = [snapshot("stable", manifest("1.0.0")),
                 snapshot("preview", manifest("1.1.0", 1, 2)),
                 snapshot("stable", manifest("1.2.0", 2))]
        with patch("release_upgrade_plan.generation", return_value={"bridge_version": "1.1.0"}):
            result = plan(manifest("1.3.0-rc.1", 2), self.registry, feeds)
        source = next(case for case in result["cases"] if case["source_version"] == "1.0.0")
        self.assertEqual([step["version"] for step in source["steps"]], ["1.1.0", "1.3.0-rc.1"])

    def test_missing_or_withdrawn_bridge_refuses_the_plan(self):
        for bridge in (None, {**manifest("1.1.0", 1, 2), "withdrawn": True}):
            feeds = [snapshot("stable", manifest("1.0.0"))]
            if bridge:
                feeds.append(snapshot("preview", bridge))
            with patch("release_upgrade_plan.generation", return_value={"bridge_version": "1.1.0"}):
                with self.assertRaises(ValueError):
                    plan(manifest("1.3.0-rc.1", 2), self.registry, feeds)

    def test_invalid_stable_and_duplicate_feeds_are_refused(self):
        bad = snapshot("stable", manifest("1.0.1-rc.1"))
        for feeds in ([bad], [snapshot("stable", manifest("1.0.0"))] * 2):
            with self.assertRaises(ValueError):
                plan(manifest("1.1.0"), self.registry, feeds)

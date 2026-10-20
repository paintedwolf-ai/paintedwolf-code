"""Retained automatic clients remain covered after channel pointers advance."""
import unittest
from unittest.mock import patch

from release_upgrade.plan import plan
from verification_tests.distribution.test_upgrade_plan_execution import manifest, snapshot


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

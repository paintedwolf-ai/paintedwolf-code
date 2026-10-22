"""Partial qualification receipts cannot delay a shard behind timeout estimates."""
import copy
import unittest

from ci_policy import hosted_plan, hosted_schedule


class HostedScheduleTests(unittest.TestCase):
    def test_cancelled_qualification_uses_one_basis_for_the_whole_matrix(self):
        # Qualification 39672112947 recorded shard 1 but ended before shard 2.
        measured = {"lanes": {"behavior 1/2": 1287.2, "limits": 226.1, "lint": 822.5}}
        original = copy.deepcopy(measured)
        rows = hosted_plan.matrix("integration", timings=measured)["include"]
        self.assertEqual(measured, original)
        self.assertEqual({(row["lane"], row["shard"]) for row in rows[:4]},
                         {("limits", ""), ("lint", ""), ("behavior", "1/2"), ("behavior", "2/2")})
        self.assertEqual({row["timing_basis"] for row in rows}, {"catalog-budget"})
        self.assertTrue(all(row["expected_seconds"] == row["minutes"] * 60 for row in rows))
        self.assertEqual(rows, hosted_plan.matrix("integration")["include"])
        self.assertEqual(hosted_plan.max_parallel("integration"), 4)

    def test_balanced_shards_share_the_longest_measurement(self):
        baseline = hosted_plan.matrix("integration")["include"]
        seconds = {row["lane"] + (f" {row['shard']}" if row["shard"] else ""): 600 for row in baseline}
        seconds.update({"behavior 1/2": 60, "behavior 2/2": 1500})
        rows = hosted_plan.matrix("integration", timings={"lanes": seconds})["include"]
        self.assertEqual([row["lane"] for row in rows[2:4]], ["behavior", "behavior"])
        self.assertEqual([row["expected_seconds"] for row in rows[2:4]], [1500, 1500])
        self.assertEqual({row["timing_basis"] for row in rows}, {"measured"})
        self.assertEqual(rows, hosted_plan.matrix("integration", timings={"lanes": seconds})["include"])

    def test_selected_profile_only_requires_its_own_rows(self):
        rows = hosted_plan.matrix("fast", timings={"lanes": {"ready": 300}})["include"]
        self.assertEqual([(row["lane"], row["expected_seconds"], row["timing_basis"]) for row in rows],
                         [("ready", 300, "measured")])

    def test_invalid_measurements_use_catalog_budgets(self):
        for value in [True, False, 0, -1, float("nan"), float("inf"), "300", None]:
            with self.subTest(value=value):
                rows = hosted_plan.matrix("fast", timings={"lanes": {"ready": value}})["include"]
                self.assertEqual(rows[0]["timing_basis"], "catalog-budget")
                self.assertEqual(rows[0]["expected_seconds"], rows[0]["minutes"] * 60)

    def test_summary_explains_fallback_and_sources(self):
        measured = {"runs": [30, 20], "lanes": {"behavior 1/2": 1287.2}}
        matrix = hosted_plan.matrix("integration", timings=measured)
        summary = hosted_schedule.summarize(matrix, measured)
        self.assertIn("Estimate basis: `catalog-budget`", summary)
        self.assertIn("`behavior 2/2`", summary)
        self.assertIn("Qualification timing sources: 30, 20", summary)
        self.assertIn("| 3 | behavior | 1/2 | 9000 |", summary)

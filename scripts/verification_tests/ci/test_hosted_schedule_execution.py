"""Partial nightly receipts cannot delay a shard behind timeout estimates."""
import copy
import unittest

from ci_policy import hosted_plan, hosted_schedule
from verification_tests.ci.fixture_plan import live

BEHAVIOR = live(phase="affected-packages")
BUILD = live(runs="check:drift")
DEN = live(runs="den:typecheck")
LINT = live(runs="test:fail-messages:check")
ADMISSION = live(phase="admission")
LINT_ADMISSION = live(runs="arch-lint:changes", area="always")
ADMISSION_TESTS = live(phase="admission-tests")
RUNNER = live(area="scripts")


class HostedScheduleTests(unittest.TestCase):
    def test_incomplete_nightly_uses_one_basis_for_the_whole_matrix(self):
        measured = {"lanes": {f"{BEHAVIOR} 1/2": 1287.2, BUILD: 226.1, LINT: 822.5}}
        original = copy.deepcopy(measured)
        rows = hosted_plan.matrix("integration", timings=measured)["include"]
        self.assertEqual(measured, original)
        self.assertEqual({row["lane"] for row in rows[:3]}, {BUILD, LINT, RUNNER})
        self.assertEqual({row["timing_basis"] for row in rows}, {"catalog-budget"})
        self.assertTrue(all(row["expected_seconds"] == row["minutes"] * 60 for row in rows))
        self.assertEqual(rows, hosted_plan.matrix("integration")["include"])
        self.assertEqual(hosted_plan.max_parallel("integration"), 6)

    def test_balanced_shards_share_the_longest_measurement(self):
        baseline = hosted_plan.matrix("integration")["include"]
        seconds = {row["timing_row"]: 600 for row in baseline}
        seconds.update({f"{BEHAVIOR} 1/2": 60, f"{BEHAVIOR} 2/2": 1500})
        rows = hosted_plan.matrix("integration", timings={"lanes": seconds})["include"]
        self.assertEqual([row["lane"] for row in rows[:3]], [BUILD, LINT, RUNNER])
        self.assertEqual([row["lane"] for row in rows[3:5]], [BEHAVIOR, BEHAVIOR])
        self.assertEqual([row["expected_seconds"] for row in rows[3:5]], [1500, 1500])
        self.assertEqual({row["timing_basis"] for row in rows}, {"measured"})
        self.assertEqual(rows, hosted_plan.matrix("integration", timings={"lanes": seconds})["include"])

    def test_selected_profile_only_requires_its_own_rows(self):
        shards = {f"{ADMISSION_TESTS} {index}/4": 400 for index in range(1, 5)}
        rows = hosted_plan.matrix("fast", timings={"lanes": {ADMISSION: 300, LINT_ADMISSION: 200, DEN: 700, **shards}})["include"]
        self.assertEqual([(row["lane"], row["expected_seconds"], row["timing_basis"]) for row in rows],
                         [(DEN, 700, "measured"), *[(ADMISSION_TESTS, 400, "measured")] * 4,
                          (ADMISSION, 300, "measured"), (LINT_ADMISSION, 200, "measured")])

    def test_invalid_measurements_use_catalog_budgets(self):
        for value in [True, False, 0, -1, float("nan"), float("inf"), "300", None]:
            with self.subTest(value=value):
                rows = hosted_plan.matrix("fast", timings={"lanes": {ADMISSION: value, LINT_ADMISSION: 200, f"{ADMISSION_TESTS} 1/2": 300, f"{ADMISSION_TESTS} 2/2": 300}})["include"]
                self.assertEqual(rows[0]["timing_basis"], "catalog-budget")
                self.assertEqual(rows[0]["expected_seconds"], rows[0]["minutes"] * 60)

    def test_an_empty_selection_summarizes_without_a_schedule(self):
        self.assertIn("No lanes", hosted_schedule.summarize({"include": []}))

    def test_summary_explains_fallback_and_sources(self):
        measured = {"run": 30, "lanes": {f"{BEHAVIOR} 1/2": 1287.2}}
        matrix = hosted_plan.matrix("integration", timings=measured)
        summary = hosted_schedule.summarize(matrix, measured)
        self.assertIn("Estimate basis: `catalog-budget`", summary)
        self.assertIn(f"`{BEHAVIOR} 2/2`", summary)
        self.assertIn("Nightly timing source: run 30.", summary)
        self.assertIn(f"| 3 | {RUNNER} |  | reject | 3600 |", summary)
        self.assertIn(f"| 4 | {BEHAVIOR} | 1/2 | critical | 9000 |", summary)

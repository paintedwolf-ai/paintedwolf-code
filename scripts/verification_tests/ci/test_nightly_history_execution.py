"""Nightly's published timings cover affected integration plans."""
import unittest

from ci_policy import hosted_plan


class NightlyHistoryTests(unittest.TestCase):
    def test_nightly_rows_measure_affected_integration(self):
        measured = {"lanes": {row["timing_row"]: 1500 if row["lane"] == "behavior" else 600
                              for row in hosted_plan.matrix("nightly")["include"]}, "packages": {}}
        scope = {"full": False, "paths": ["lycaon/internal/workflow/plan.go"]}
        matrix = hosted_plan.matrix("integration", scope=scope, timings=measured)
        self.assertEqual({row["timing_basis"] for row in matrix["include"]}, {"measured"})
        build = next(row for row in matrix["include"] if row["lane"] == "build")
        self.assertEqual((build["timing_row"], build["expected_seconds"]), ("build", 600))
        self.assertEqual([row["lane"] for row in matrix["include"][:4]], ["build", "lint", "behavior", "behavior"])
        del measured["lanes"]["behavior 2/2"]
        fallback = hosted_plan.matrix("integration", scope=scope, timings=measured)
        self.assertEqual({row["timing_basis"] for row in fallback["include"]}, {"catalog-budget"})


if __name__ == "__main__":
    unittest.main()

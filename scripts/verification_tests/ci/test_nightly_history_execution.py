"""Nightly's published timings cover affected integration plans."""
import unittest

from ci_policy import hosted_plan
from verification_tests.ci.fixture_plan import live

BEHAVIOR = live(phase="affected-packages")
BUILD = live(runs="check:drift")
SHARDS = hosted_plan.lanes()[BEHAVIOR]["shards"]


class NightlyHistoryTests(unittest.TestCase):
    def test_nightly_rows_measure_affected_integration(self):
        measured = {"lanes": {row["timing_row"]: 1500 if row["lane"] == BEHAVIOR else 600
                              for row in hosted_plan.matrix("nightly")["include"]}, "packages": {}}
        scope = {"full": False, "paths": ["lycaon/internal/egressclass/egressclass.go"]}
        matrix = hosted_plan.matrix("integration", scope=scope, timings=measured)
        self.assertEqual({row["timing_basis"] for row in matrix["include"]}, {"measured"})
        build = next(row for row in matrix["include"] if row["lane"] == BUILD)
        self.assertEqual((build["timing_row"], build["expected_seconds"]), (BUILD, 600))
        self.assertEqual([row["lane"] for row in matrix["include"][:1 + SHARDS]], [BUILD] + [BEHAVIOR] * SHARDS)
        # Nightly cannot run the lane scoped to the change's own packages; its budget stands alone.
        race = next(row for row in matrix["include"] if row["lane"] == live(phase="changed-packages"))
        self.assertEqual(race["expected_seconds"], race["minutes"] * 60)
        del measured["lanes"][f"{BEHAVIOR} 2/{SHARDS}"]
        fallback = hosted_plan.matrix("integration", scope=scope, timings=measured)
        self.assertEqual({row["timing_basis"] for row in fallback["include"]}, {"catalog-budget"})


if __name__ == "__main__":
    unittest.main()

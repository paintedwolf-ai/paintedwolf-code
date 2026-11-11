"""Nightly receipts cover affected integration plans through declared timing sources."""
import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import hosted_plan, hosted_schedule, timings


class NightlyHistoryTests(unittest.TestCase):
    def test_healthy_nightly_stops_history_and_measures_affected_integration(self):
        qualified = hosted_plan.matrix("nightly")["include"]
        required = {row["timing_row"] for row in qualified}
        names = [f"receipt-verification-nightly-{index}"
                 for index, row in enumerate(qualified)]

        def download(repository, run, artifacts, directory):
            for name, row in zip(artifacts, qualified):
                path = Path(directory) / name / "ci/timings.json"
                path.parent.mkdir(parents=True)
                path.write_text(json.dumps({"basis": "setup-and-execution", "lane": row["lane"], "shard": row["shard"], "status": "passed",
                                            "seconds": 1500 if row["lane"] == "behavior" else 600, "packages": {}}))

        with patch.object(timings.github, "api", return_value={"workflow_runs": [{"id": 3}, {"id": 2}]}), \
                patch.object(timings.github, "pages", return_value=[{"name": name, "expired": False} for name in names]) as pages, \
                patch.object(timings, "download", side_effect=download) as downloaded:
            measured = timings.latest("example/repository")
        self.assertEqual(downloaded.call_count, 1)
        self.assertEqual(pages.call_count, 1)
        self.assertEqual(set(measured["lanes"]), required)
        scope = {"full": False, "paths": ["lycaon/internal/workflow/plan.go"]}
        matrix = hosted_plan.matrix("integration", scope=scope, timings=measured)
        self.assertEqual({row["timing_basis"] for row in matrix["include"]}, {"measured"})
        build = next(row for row in matrix["include"] if row["lane"] == "build")
        self.assertEqual((build["timing_row"], build["expected_seconds"]), ("build", 600))
        self.assertEqual([row["lane"] for row in matrix["include"][:4]], ["limits", "lint", "behavior", "behavior"])
        del measured["lanes"]["behavior 2/2"]
        fallback = hosted_plan.matrix("integration", scope=scope, timings=measured)
        self.assertEqual({row["timing_basis"] for row in fallback["include"]}, {"catalog-budget"})

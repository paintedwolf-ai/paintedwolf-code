import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import hosted_plan, timings
import verification_plan as planning


def events(root, batch, rows):
    path = root / "verification" / batch / "stage-001-events.jsonl"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("".join(json.dumps(row) + "\n" for row in rows))


class MeasuredTimingsTests(unittest.TestCase):
    def test_sharded_lane_records_its_measured_packages(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "ci").mkdir()
            events(root, "a", [{"package": "p/one", "elapsed": 4.0, "reused": False},
                               {"package": "p/two", "elapsed": 9.5, "reused": True},
                               {"package": "p/three", "exit_code": 1}])
            events(root, "b", [{"package": "p/one", "elapsed": 1.25, "reused": False}])
            run = {"lane": "behavior", "shard": "1/2", "status": "passed", "started_at": 100.0, "finished_at": 160.0}
            timings.record(root, run)
            measured = json.loads((root / "ci/timings.json").read_text())
            self.assertEqual(measured, {"lane": "behavior", "shard": "1/2", "status": "passed", "seconds": 60.0,
                                        "packages": {"p/one": 5.25}})
            timings.record(root, {**run, "lane": "lint", "shard": ""})
            self.assertEqual(json.loads((root / "ci/timings.json").read_text())["packages"], {})

    def test_merge_keeps_passed_lane_rows_and_every_measured_package(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            rows = [{"lane": "behavior", "shard": "1/2", "status": "passed", "seconds": 900, "packages": {"a": 1}},
                    {"lane": "behavior", "shard": "2/2", "status": "failed", "seconds": 300, "packages": {"b": 2}},
                    {"lane": "lint", "shard": "", "status": "passed", "seconds": 600, "packages": {}}]
            paths = []
            for index, row in enumerate(rows):
                path = root / f"{index}.json"
                path.write_text(json.dumps(row))
                paths.append(path)
            self.assertEqual(timings.merge(paths), {"lanes": {"behavior 1/2": 900, "lint": 600},
                                                    "packages": {"a": 1, "b": 2}})

    def test_latest_reads_the_newest_qualification_that_recorded_timings(self):
        runs = {"workflow_runs": [{"id": 3}, {"id": 2}, {"id": 1}]}
        artifacts = {3: [{"name": "verification-check-lint-1", "expired": False}],
                     2: [{"name": "receipt-verification-check-lint-1", "expired": False},
                         {"name": "receipt-verification-check-behavior-2-1", "expired": False},
                         {"name": "receipt-verification-check-runner-1", "expired": True}],
                     1: [{"name": "receipt-verification-check-lint-1", "expired": False}]}
        downloaded = []

        def download(repository, run, names, directory):
            downloaded.append((run, names))
            for name in names:
                path = Path(directory) / name / "ci" / "timings.json"
                path.parent.mkdir(parents=True)
                lane = "behavior" if "behavior" in name else "lint"
                path.write_text(json.dumps({"lane": lane, "shard": "1/2" if lane == "behavior" else "",
                                            "status": "passed", "seconds": 60, "packages": {}}))

        with patch.object(timings.github, "api", return_value=runs), \
                patch.object(timings.github, "pages", side_effect=lambda path, key: artifacts[int(path.split("/")[-2])]), \
                patch.object(timings, "download", side_effect=download):
            measured = timings.latest("owner/repo")
        self.assertEqual(downloaded, [(2, ["receipt-verification-check-lint-1", "receipt-verification-check-behavior-2-1"])])
        self.assertEqual(measured, {"run": 2, "lanes": {"lint": 60, "behavior 1/2": 60}, "packages": {}})

    def test_unavailable_measurements_plan_without_them(self):
        refused = subprocess.CalledProcessError(1, ["gh"], stderr="HTTP 403: Resource not accessible")
        with patch.object(timings, "latest", side_effect=refused), patch("builtins.print") as warn:
            self.assertEqual(timings.resolve("owner/repo"), {})
        self.assertIn("HTTP 403", warn.call_args.args[0])

    def test_measured_lanes_order_the_capped_matrix(self):
        lanes = hosted_plan.lanes()
        unmeasured = hosted_plan.matrix("integration")["include"]
        measured = {row["lane"] + (f" {row['shard']}" if row["shard"] else ""): 60 for row in unmeasured}
        measured.update({"contracts": 300, "frontend": 800, "native": 600, "runner": 320})
        del measured["webkit-gtk"]
        rows = hosted_plan.matrix("integration", timings={"lanes": measured, "packages": {}})["include"]
        self.assertEqual(sorted(map(json.dumps, rows)), sorted(map(json.dumps, unmeasured)))
        self.assertEqual({row["lane"] for row in rows[:2]}, {"limits", "lint"})
        rest = [row["lane"] for row in rows if not lanes[row["lane"]].get("first")]
        # An unmeasured lane is planned at its budget, so it starts early.
        self.assertEqual(rest[:4], ["webkit-gtk", "frontend", "native", "runner"])
        self.assertLess(rest.index("runner"), rest.index("contracts"))

    def test_plan_without_measurements_orders_by_budget(self):
        data = copy.deepcopy(planning.catalog())
        with patch.object(hosted_plan, "catalog", return_value=data):
            rows = hosted_plan.matrix("check", timings={})["include"]
        lanes = hosted_plan.lanes()
        order = [(not lanes[row["lane"]].get("first"), -row["minutes"]) for row in rows]
        self.assertEqual(order, sorted(order))


if __name__ == "__main__":
    unittest.main()

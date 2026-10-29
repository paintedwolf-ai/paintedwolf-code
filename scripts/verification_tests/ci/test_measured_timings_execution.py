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
            run = {"lane": "behavior", "shard": "1/2", "status": "passed", "job_started_at": 80.0, "started_at": 100.0, "finished_at": 160.0}
            timings.record(root, run)
            measured = json.loads((root / "ci/timings.json").read_text())
            self.assertEqual(measured, {"basis": "setup-and-execution", "lane": "behavior", "shard": "1/2", "status": "passed", "seconds": 80.0,
                                        "packages": {"p/one": 5.25}})
            timings.record(root, {**run, "lane": "lint", "shard": ""})
            self.assertEqual(json.loads((root / "ci/timings.json").read_text())["packages"], {})

    def test_execution_only_history_cannot_mix_with_setup_inclusive_estimates(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'ci').mkdir()
            run = {'lane': 'native', 'shard': '', 'status': 'passed', 'started_at': 100, 'finished_at': 150}
            timings.record(root, run)
            self.assertEqual(timings.merge([root / 'ci/timings.json']), {'lanes': {}, 'packages': {}})
            timings.record(root, {**run, 'job_started_at': 200})
            self.assertEqual(timings.merge([root / 'ci/timings.json'])['lanes'], {})
            timings.record(root, {**run, 'job_started_at': 50})
            self.assertEqual(timings.merge([root / 'ci/timings.json'])['lanes'], {'native': 100})

    def test_merge_keeps_passed_lane_rows_and_every_measured_package(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            rows = [{"lane": "behavior", "shard": "1/2", "status": "passed", "seconds": 900, "packages": {"a": 1}},
                    {"lane": "behavior", "shard": "2/2", "status": "failed", "seconds": 300, "packages": {"b": 2}},
                    {"lane": "lint", "shard": "", "status": "passed", "seconds": 600, "packages": {}}]
            paths = []
            for index, row in enumerate(rows):
                path = root / f"{index}.json"
                path.write_text(json.dumps({"basis": "setup-and-execution", **row}))
                paths.append(path)
            self.assertEqual(timings.merge(paths), {"lanes": {"behavior 1/2": 900, "lint": 600},
                                                    "packages": {"a": 1, "b": 2}})

    def test_invalid_durations_cannot_poison_history(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "timings.json"
            for seconds in [True, 0, -1, float("nan"), float("inf"), "60", None]:
                with self.subTest(seconds=seconds):
                    path.write_text(json.dumps({"basis": "setup-and-execution", "lane": "behavior", "shard": "1/2", "status": "passed",
                                                "seconds": seconds,
                                                "packages": {"valid": 2, "zero": 0, "negative": -1,
                                                             "infinite": float("inf"), "boolean": True}}))
                    self.assertEqual(timings.merge([path]),
                                     {"lanes": {}, "packages": {"valid": 2, "zero": 0}})

    def test_history_stops_once_required_rows_are_available(self):
        runs = {"workflow_runs": [{"id": 3}, {"id": 2}, {"id": 1}]}
        artifacts = {3: [{"name": "verification-nightly-lint-1", "expired": False}],
                     2: [{"name": "receipt-verification-nightly-lint-1", "expired": False},
                         {"name": "receipt-verification-nightly-behavior-2-1", "expired": False},
                         {"name": "receipt-verification-nightly-runner-1", "expired": True}],
                     1: [{"name": "receipt-verification-nightly-lint-1", "expired": False}]}
        downloaded = []

        def download(repository, run, names, directory):
            downloaded.append((run, names))
            for name in names:
                path = Path(directory) / name / "ci" / "timings.json"
                path.parent.mkdir(parents=True)
                lane = "behavior" if "behavior" in name else "lint"
                path.write_text(json.dumps({"basis": "setup-and-execution", "lane": lane, "shard": "1/2" if lane == "behavior" else "",
                                            "status": "passed", "seconds": 60, "packages": {}}))

        with patch.object(timings.github, "api", return_value=runs), \
                patch.object(timings.github, "pages", side_effect=lambda path, key: artifacts[int(path.split("/")[-2])]), \
                patch.object(timings, "download", side_effect=download), \
                patch.object(timings, "nightly_rows", return_value={"lint", "behavior 1/2"}):
            measured = timings.latest("owner/repo")
        self.assertEqual(downloaded, [(2, ["receipt-verification-nightly-lint-1", "receipt-verification-nightly-behavior-2-1"])])
        self.assertEqual(measured, {"runs": [2], "lanes": {"lint": 60, "behavior 1/2": 60},
                                    "packages": {}, "lane_runs": {"lint": 2, "behavior 1/2": 2}})

    def test_history_fills_missing_rows_without_replacing_newer_measurements(self):
        runs = {"workflow_runs": [{"id": 3}, {"id": 2}, {"id": 1}]}
        data = {3: [{"lane": "behavior", "shard": "1/2", "status": "passed", "seconds": 1287.2,
                     "packages": {"a": 3}}],
                2: [{"lane": "behavior", "shard": "1/2", "status": "passed", "seconds": 1403.5,
                     "packages": {"a": 8}},
                    {"lane": "behavior", "shard": "2/2", "status": "passed", "seconds": 1279.7,
                     "packages": {"b": 5}},
                    {"lane": "webkit-gtk", "shard": "", "status": "passed", "seconds": 450,
                     "packages": {}}]}
        downloaded = []

        def artifacts(path, key):
            run = int(path.split("/")[-2])
            return [{"name": f"receipt-verification-nightly-{index}", "expired": False}
                    for index, row in enumerate(data[run])]

        def download(repository, run, names, directory):
            downloaded.append(run)
            for index, row in enumerate(data[run]):
                path = Path(directory) / names[index] / "ci" / "timings.json"
                path.parent.mkdir(parents=True)
                path.write_text(json.dumps({"basis": "setup-and-execution", **row}))

        with patch.object(timings.github, "api", return_value=runs) as api, \
                patch.object(timings.github, "pages", side_effect=artifacts), \
                patch.object(timings, "download", side_effect=download), \
                patch.object(timings, "nightly_rows", return_value={"behavior 1/2", "behavior 2/2", "webkit-gtk"}):
            measured = timings.latest("example/repository")
        self.assertIn("per_page=20", api.call_args.args[0])
        self.assertEqual(downloaded, [3, 2])
        self.assertEqual(measured, {"runs": [3, 2], "lanes": {"behavior 1/2": 1287.2, "behavior 2/2": 1279.7, "webkit-gtk": 450},
                                    "packages": {"a": 3, "b": 5},
                                    "lane_runs": {"behavior 1/2": 3, "behavior 2/2": 2, "webkit-gtk": 2}})

    def test_history_requires_only_rows_produced_by_nightly(self):
        expected = {timings.row_key(row["lane"], row["shard"])
                    for matrix in (hosted_plan.matrix("nightly"),)
                    for row in matrix["include"]}
        self.assertEqual(timings.nightly_rows(), expected)
        self.assertTrue({"build", "webkit-gtk", "confinement-arm64"} <= expected)
        self.assertNotIn("integration-build", expected)

    def test_unavailable_measurements_plan_without_them(self):
        refused = subprocess.CalledProcessError(1, ["gh"], stderr="HTTP 403: Resource not accessible")
        with patch.object(timings, "latest", side_effect=refused), patch("builtins.print") as warn:
            self.assertEqual(timings.resolve("owner/repo"), {})
        self.assertIn("HTTP 403", warn.call_args.args[0])

    def test_measured_lanes_order_the_capped_matrix(self):
        lanes = hosted_plan.lanes()
        unmeasured = hosted_plan.matrix("integration")["include"]
        measured = {row["timing_row"]: 60 for row in unmeasured}
        measured.update({"contracts": 300, "frontend": 800, "native": 600, "runner": 320, "webkit-gtk": 900})
        rows = hosted_plan.matrix("integration", timings={"lanes": measured, "packages": {}})["include"]
        selection = lambda rows: sorted((row["lane"], row["shard"], row["minutes"]) for row in rows)
        self.assertEqual(selection(rows), selection(unmeasured))
        self.assertEqual({row["lane"] for row in rows[:2]}, {"limits", "lint"})
        rest = [row["lane"] for row in rows if lanes[row["lane"]].get("priority", "normal") in {"compile", "normal"}]
        self.assertEqual(rest[:4], ["native", "webkit-gtk", "frontend", "runner"])
        self.assertLess(rest.index("runner"), rest.index("contracts"))

    def test_plan_without_measurements_orders_by_budget(self):
        data = copy.deepcopy(planning.catalog())
        with patch.object(hosted_plan, "catalog", return_value=data):
            rows = hosted_plan.matrix("check", timings={})["include"]
        lanes = hosted_plan.lanes()
        order = [(hosted_plan.PRIORITIES[row["priority"]], -row["minutes"])
                 for row in rows if row["priority"] != "compile"]
        self.assertEqual(order, sorted(order))


if __name__ == "__main__":
    unittest.main()

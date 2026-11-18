import copy
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import zipfile

from ci_policy import hosted_plan, timings
import verification_plan as planning
from verification_tests.ci.fixture_plan import live

BEHAVIOR = live(phase="affected-packages")
BENCHMARKS = live(runs="perf:bench", profiles=["nightly"])
BUILD = live(runs="check:drift")
DEN = live(runs="den:typecheck")
LINT = live(runs="test:fail-messages:check")
NATIVE = live(runs="den:test:rust")
RACE = live(runs="test:race", profiles=["nightly"])
RUNNER = live(area="scripts")
WEBKIT_GTK = live(runs="den:webkit:atspi")


def events(root, batch, rows):
    path = root / "verification" / batch / "stage-001-events.jsonl"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("".join(json.dumps(row) + "\n" for row in rows))


class MeasuredTimingsTests(unittest.TestCase):
    def test_sharded_lane_records_its_measured_packages(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "ci").mkdir()
            events(root, "a", [{"package": "p/one", "elapsed": 4.0},
                               {"package": "p/three", "exit_code": 1}])
            events(root, "b", [{"package": "p/one", "elapsed": 1.25}])
            run = {"lane": BEHAVIOR, "shard": "1/2", "status": "passed", "exit_code": 0,
                   "job_started_at": 80.0, "started_at": 100.0, "finished_at": 160.0}
            timings.record(root, run)
            measured = json.loads((root / "ci/timings.json").read_text())
            self.assertEqual(measured, {"basis": "setup-and-execution", "lane": BEHAVIOR, "shard": "1/2", "status": "passed",
                                        "finished": True, "seconds": 80.0, "packages": {"p/one": 5.25}, "tests": []})
            timings.record(root, {**run, "lane": LINT, "shard": ""})
            self.assertEqual(json.loads((root / "ci/timings.json").read_text())["packages"], {})

    def test_execution_only_history_cannot_mix_with_setup_inclusive_estimates(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'ci').mkdir()
            run = {'lane': NATIVE, 'shard': '', 'status': 'passed', 'exit_code': 0, 'started_at': 100, 'finished_at': 150}
            timings.record(root, run)
            self.assertEqual(timings.merge([root / 'ci/timings.json']), {'lanes': {}, 'packages': {}, 'tests': []})
            timings.record(root, {**run, 'job_started_at': 200})
            self.assertEqual(timings.merge([root / 'ci/timings.json'])['lanes'], {})
            timings.record(root, {**run, 'job_started_at': 50})
            self.assertEqual(timings.merge([root / 'ci/timings.json'])['lanes'], {NATIVE: 100})

    def test_merge_keeps_finished_lane_rows_and_every_measured_package(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            rows = [{"lane": BEHAVIOR, "shard": "1/2", "status": "passed", "finished": True, "seconds": 900, "packages": {"a": 1}},
                    {"lane": BEHAVIOR, "shard": "2/2", "status": "failed", "finished": True, "seconds": 300, "packages": {"b": 2}},
                    {"lane": BENCHMARKS, "shard": "", "status": "unverified", "finished": True, "seconds": 1500, "packages": {}},
                    {"lane": RACE, "shard": "1/3", "status": "unverified", "finished": False, "seconds": 1800, "packages": {"a": 25}},
                    {"lane": LINT, "shard": "", "status": "passed", "seconds": 600, "packages": {}}]
            paths = []
            for index, row in enumerate(rows):
                path = root / f"{index}.json"
                path.write_text(json.dumps({"basis": "setup-and-execution", **row}))
                paths.append(path)
            self.assertEqual(timings.merge(paths), {"lanes": {f"{BEHAVIOR} 1/2": 900, f"{BEHAVIOR} 2/2": 300, BENCHMARKS: 1500},
                                                    "packages": {"a": 1, "b": 2}, "tests": []})

    def test_a_lane_ended_by_a_signal_is_not_a_finished_measurement(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "ci").mkdir()
            run = {"lane": RACE, "shard": "1/3", "job_started_at": 0.0, "started_at": 10.0, "finished_at": 70.0}
            for exit_code, finished in ((0, True), (1, True), (201, True), (-15, False), (None, False)):
                with self.subTest(exit_code=exit_code):
                    timings.record(root, {**run, "status": "failed", "exit_code": exit_code})
                    self.assertIs(json.loads((root / "ci/timings.json").read_text())["finished"], finished)

    def test_invalid_durations_cannot_poison_history(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "timings.json"
            for seconds in [True, 0, -1, float("nan"), float("inf"), "60", None]:
                with self.subTest(seconds=seconds):
                    path.write_text(json.dumps({"basis": "setup-and-execution", "lane": BEHAVIOR, "shard": "1/2", "status": "passed",
                                                "finished": True, "seconds": seconds,
                                                "packages": {"valid": 2, "zero": 0, "negative": -1,
                                                             "infinite": float("inf"), "boolean": True},
                                                "tests": [{"package": "p", "test": "TestSlow", "seconds": seconds}]}))
                    self.assertEqual(timings.merge([path]),
                                     {"lanes": {}, "packages": {"valid": 2, "zero": 0}, "tests": []})

    def test_lane_records_its_slowest_top_level_tests(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "ci").mkdir()
            stream = root / "verification" / "a" / "stage-001-go.json"
            stream.parent.mkdir(parents=True)
            finished = [{"Action": "pass", "Package": "p/one", "Test": f"Test{index:03}", "Elapsed": index}
                        for index in range(1, timings.SLOWEST_TESTS + 3)]
            ignored = [{"Action": "run", "Package": "p/one", "Test": "TestStarted"},
                       {"Action": "pass", "Package": "p/one", "Test": "TestParent/child", "Elapsed": 900},
                       {"Action": "pass", "Package": "p/one", "Elapsed": 950},
                       {"Action": "fail", "Package": "p/two", "Test": "TestBroken", "Elapsed": 0.0}]
            stream.write_text("not json\n[1]\n" + "".join(json.dumps(row) + "\n" for row in finished + ignored))
            run = {"lane": LINT, "shard": "", "status": "failed", "exit_code": 1,
                   "job_started_at": 80.0, "started_at": 100.0, "finished_at": 160.0}
            timings.record(root, run)
            tests = json.loads((root / "ci/timings.json").read_text())["tests"]
        self.assertEqual(len(tests), timings.SLOWEST_TESTS)
        self.assertEqual(tests[0], {"package": "p/one", "test": f"Test{timings.SLOWEST_TESTS + 2:03}", "seconds": timings.SLOWEST_TESTS + 2})
        self.assertEqual(tests[-1]["seconds"], 3)

    def test_run_publishes_one_file_bound_to_its_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            rows = {"receipt-verification-nightly-lint-1": {"lane": LINT, "shard": "", "status": "failed", "seconds": 700, "packages": {},
                                                             "tests": [{"package": "p", "test": "TestSlow", "seconds": 4}]},
                    "receipt-verification-nightly-lint-2": {"lane": LINT, "shard": "", "status": "passed", "seconds": 600, "packages": {},
                                                             "tests": [{"package": "p", "test": "TestSlow", "seconds": 9},
                                                                       {"package": "q", "test": "TestSlower", "seconds": 12}]},
                    "receipt-verification-nightly-behavior-0-1": {"lane": BEHAVIOR, "shard": "1/2", "status": "passed", "seconds": 900,
                                                                   "packages": {"a": 3}}}
            for index, (name, row) in enumerate(rows.items()):
                # The artifact service may root a receipt at its common directory, `ci/`.
                path = root / "receipts" / name / ("ci" if index else "") / "timings.json"
                path.parent.mkdir(parents=True)
                path.write_text(json.dumps({"basis": "setup-and-execution", "finished": True, **row}))
            browser = {"receipt-playwright-web-1-1": {"boot.spec.ts": 30, "prompt.spec.ts": 7},
                       "receipt-playwright-web-1-2": {"boot.spec.ts": 12.5, "broken.spec.ts": "9"},
                       "receipt-playwright-web-2-1": {"files.spec.ts": 40}}
            for name, measured in browser.items():
                path = root / "receipts" / name / "e2e-specs-1.json"
                path.parent.mkdir(parents=True)
                path.write_text(json.dumps(measured))
            environment = {"GITHUB_RUN_ID": "77", "SOURCE_SHA": "a" * 40, "SOURCE_TREE": "b" * 40}
            with patch.dict(timings.os.environ, environment), patch("builtins.print") as said:
                self.assertEqual(timings.main([str(root / "receipts"), str(root / "timings.json")]), 0)
            published = json.loads((root / "timings.json").read_text())
        self.assertEqual(published, {"run": 77, "source_sha": "a" * 40, "source_tree": "b" * 40,
                                     "lanes": {LINT: 600, f"{BEHAVIOR} 1/2": 900}, "packages": {"a": 3},
                                     "specs": {"boot.spec.ts": 12.5, "files.spec.ts": 40, "prompt.spec.ts": 7},
                                     "tests": [{"package": "q", "test": "TestSlower", "seconds": 12},
                                               {"package": "p", "test": "TestSlow", "seconds": 9}]})
        self.assertIn("2 lane, 1 package, 2 test and 3 browser spec durations", said.call_args.args[0])

    def test_browser_shard_records_each_spec_file_with_its_retries(self):
        report = {"suites": [
            {"file": "boot.spec.ts", "specs": [{"tests": [{"results": [{"duration": 1500}, {"duration": 2500}]}]}],
             "suites": [{"specs": [{"tests": [{"results": [{"duration": 1000}]}]}]}]},
            {"file": "journey.spec.ts", "suites": [{"suites": [{"specs": [{"tests": [{"results": [{"duration": 250}]}]}]}]}]},
            {"file": "skipped.spec.ts", "specs": [{"tests": [{"results": []}]}]},
            {"file": "odd.spec.ts", "specs": [{"tests": [{"results": [{"duration": True}, {"duration": -5}]}]}]}]}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "report.json").write_text(json.dumps(report))
            self.assertEqual(timings.main(["specs", str(root / "report.json"), str(root / "ci/e2e-specs-2.json")]), 0)
            self.assertEqual(json.loads((root / "ci/e2e-specs-2.json").read_text()),
                             {"boot.spec.ts": 5.0, "journey.spec.ts": 0.25})

    def archive(self, members):
        raw = io.BytesIO()
        with zipfile.ZipFile(raw, "w") as bundle:
            for name, value in members.items():
                bundle.writestr(name, value if isinstance(value, str) else json.dumps(value))
        return raw.getvalue()

    def test_plan_reads_the_newest_main_file_with_one_download(self):
        measured = {"run": 9, "lanes": {LINT: 60}, "packages": {"a": 1}, "tests": []}
        listing = {"artifacts": [
            {"id": 1, "expired": False, "created_at": "2026-11-14T02:00:00Z", "workflow_run": {"id": 7, "head_branch": "main"}},
            {"id": 2, "expired": False, "created_at": "2026-11-15T02:00:00Z", "workflow_run": {"id": 9, "head_branch": "main"}},
            {"id": 3, "expired": True, "created_at": "2026-11-16T02:00:00Z", "workflow_run": {"id": 11, "head_branch": "main"}},
            {"id": 4, "expired": False, "created_at": "2026-11-16T03:00:00Z", "workflow_run": {"id": 12, "head_branch": "topic"}}]}
        with patch.object(timings.github, "api", return_value=listing) as api, patch("builtins.print"), \
                patch.object(timings.subprocess, "check_output", return_value=self.archive({"timings.json": measured})) as download:
            self.assertEqual(timings.latest("owner/repo"), measured)
        api.assert_called_once_with("repos/owner/repo/actions/artifacts?name=nightly-timings&per_page=100")
        download.assert_called_once_with(["gh", "api", "repos/owner/repo/actions/artifacts/2/zip"])

    def test_plan_without_a_published_file_downloads_nothing(self):
        listing = {"artifacts": [{"id": 4, "expired": False, "created_at": "2026-11-16T03:00:00Z",
                                  "workflow_run": {"id": 12, "head_branch": "topic"}}]}
        with patch.object(timings.github, "api", return_value=listing), patch("builtins.print"), \
                patch.object(timings.subprocess, "check_output") as download:
            self.assertEqual(timings.resolve("owner/repo"), {})
        download.assert_not_called()

    def test_unreadable_published_files_plan_without_measurements(self):
        listing = {"artifacts": [{"id": 2, "expired": False, "created_at": "2026-11-15T02:00:00Z",
                                  "workflow_run": {"id": 9, "head_branch": "main"}}]}
        archives = [self.archive({"timings.json": {"lanes": {}, "packages": {}}, "extra.json": {}}),
                    self.archive({"other.json": {}}), self.archive({"timings.json": {"lanes": []}}),
                    self.archive({"timings.json": "x" * (timings.MAX_BYTES + 1)}), b"not a zip"]
        for raw in archives:
            with self.subTest(size=len(raw)), patch.object(timings.github, "api", return_value=listing), \
                    patch.object(timings.subprocess, "check_output", return_value=raw), patch("builtins.print") as warn:
                self.assertEqual(timings.resolve("owner/repo"), {})
                self.assertIn("Measured timings unavailable", warn.call_args.args[0])

    def test_unavailable_measurements_plan_without_them(self):
        refused = subprocess.CalledProcessError(1, ["gh"], stderr="HTTP 403: Resource not accessible")
        with patch.object(timings, "latest", side_effect=refused), patch("builtins.print") as warn:
            self.assertEqual(timings.resolve("owner/repo"), {})
        self.assertIn("HTTP 403", warn.call_args.args[0])

    def test_measured_lanes_order_the_capped_matrix(self):
        lanes = hosted_plan.lanes()
        unmeasured = hosted_plan.matrix("integration")["include"]
        measured = {row["timing_row"]: 60 for row in unmeasured}
        measured.update({BUILD: 300, DEN: 800, NATIVE: 600, RUNNER: 320, WEBKIT_GTK: 900})
        rows = hosted_plan.matrix("integration", timings={"lanes": measured, "packages": {}})["include"]
        selection = lambda rows: sorted((row["lane"], row["shard"], row["minutes"]) for row in rows)
        self.assertEqual(selection(rows), selection(unmeasured))
        self.assertEqual({row["lane"] for row in rows[:2]}, {BUILD, RUNNER})
        rest = [row["lane"] for row in rows if lanes[row["lane"]].get("priority", "normal") == "normal"]
        self.assertEqual(rest[:3], [WEBKIT_GTK, DEN, NATIVE])
        self.assertLess([row["lane"] for row in rows].index(RUNNER),
                        [row["lane"] for row in rows].index(BEHAVIOR))

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

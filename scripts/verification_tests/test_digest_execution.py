import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


class GoDigestTests(unittest.TestCase):
    def digest(self, events, directory, process_code=0):
        return subprocess.run(
            [sys.executable, str(Path(__file__).resolve().parents[1].joinpath("test-digest.py")), "--process-rc", str(process_code)],
            input="\n".join(json.dumps(event) for event in events),
            text=True, capture_output=True,
            env={**os.environ, "LAST_RUN_DIR": directory}, check=False,
        )

    def test_empty_malformed_and_incomplete_captures_never_pass(self):
        cases = [[], [[]], [{"Package": [], "Action": "pass"}],
                 [{"Package": "example/test", "Action": "start"}],
                 [{"Package": "example/test", "Test": "TestOne", "Action": "pass"}]]
        with tempfile.TemporaryDirectory() as directory:
            for events in cases:
                with self.subTest(events=events):
                    result = self.digest(events, directory)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertNotIn("✓", result.stdout)
                    self.assertNotIn("Traceback", result.stderr)

    def test_process_failure_overrides_completed_package_events(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.digest([{"Package": "example/test", "Action": "pass"}], directory, 137)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("Go exited with status 137", result.stdout)
            self.assertNotIn("✓", result.stdout)

    def test_report_distinguishes_executed_skipped_and_no_test_packages(self):
        events = [
            {"Package": "fixture/ran", "Test": "TestRan", "Action": "run"},
            {"Package": "fixture/ran", "Test": "TestRan", "Action": "pass"},
            {"Package": "fixture/ran", "Action": "pass"},
            {"Package": "fixture/skipped", "Test": "TestResource", "Action": "run"},
            {"Package": "fixture/skipped", "Test": "TestResource", "Action": "skip"},
            {"Package": "fixture/skipped", "Action": "pass"},
            {"Package": "fixture/no_tests", "Action": "skip"},
        ]
        with tempfile.TemporaryDirectory() as directory:
            result = self.digest(events, directory)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("test results: 1 passed, 0 failed, 1 skipped", result.stdout)
        self.assertIn("1 package without tests", result.stdout)

    def test_all_skipped_and_no_test_reports_never_claim_executed_tests(self):
        cases = [
            ([{"Package": "fixture/no_tests", "Action": "skip"}], 0, 1),
            ([{"Package": "fixture/skipped", "Test": "TestResource", "Action": "skip"},
              {"Package": "fixture/skipped", "Action": "pass"}], 1, 0),
        ]
        with tempfile.TemporaryDirectory() as directory:
            for events, skipped, empty in cases:
                with self.subTest(events=events):
                    result = self.digest(events, directory)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn(f"test results: 0 passed, 0 failed, {skipped} skipped", result.stdout)
                    self.assertIn(f"{empty} package", result.stdout)

    def test_skipped_package_establishes_completion(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.digest([{"Package": "example/test", "Action": "skip"}], directory)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_compiler_failure_includes_dependency_diagnostics(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.digest([
                {"ImportPath": "example/source", "Action": "build-output", "Output": "source.go:10: undefined: Missing\n"},
                {"ImportPath": "example/source", "Action": "build-fail"},
                {"Package": "example/consumer", "Action": "fail"},
            ], directory)
            self.assertEqual(result.returncode, 1)
            self.assertIn("source.go:10: undefined: Missing", result.stdout)
            self.assertIn("example/source: build failed", result.stdout)
            self.assertEqual(
                Path(directory, "failed-go-pkgs.txt").read_text().splitlines(),
                ["example/consumer", "example/source"],
            )

    def test_successful_build_diagnostics_do_not_fail_tests(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.digest([
                {"ImportPath": "example/source", "Action": "build-output", "Output": "compiler diagnostic\n"},
                {"Package": "example/consumer", "Action": "pass"},
            ], directory)
            self.assertEqual(result.returncode, 0)
            self.assertIn("1 package, no failures", result.stdout)
            self.assertFalse(Path(directory, "failed-go-pkgs.txt").exists())

    def test_timings_are_bounded_and_do_not_double_count_subtests_or_cached_runs(self):
        events = [{"Package": "example/slow", "Test": f"Test{i}", "Action": "pass", "Elapsed": i}
                  for i in range(1, 8)]
        events.extend([
            {"Package": "example/slow", "Test": "Test7/child", "Action": "pass", "Elapsed": 7},
            {"Package": "example/slow", "Action": "pass", "Elapsed": 28},
            {"Package": "example/cached", "Test": "TestCached", "Action": "pass", "Elapsed": 90},
            {"Package": "example/cached", "Action": "output", "Output": "ok  example/cached\t(cached)\n"},
            {"Package": "example/cached", "Action": "pass", "Elapsed": 100},
        ])
        with tempfile.TemporaryDirectory() as directory:
            result = self.digest(events, directory)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("28.00s  example/slow", result.stdout)
        self.assertLess(result.stdout.index("Test7"), result.stdout.index("Test3"))
        for omitted in ("Test1", "Test2", "/child", "example/cached"):
            self.assertNotIn(omitted, result.stdout)

    def test_missing_or_invalid_timings_do_not_change_failure_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            for elapsed in (None, "slow", True, -1, float("nan"), float("inf"), 10**1000):
                with self.subTest(elapsed=elapsed):
                    result = self.digest([
                        {"Package": "example/test", "Test": "TestOne", "Action": "fail", "Elapsed": elapsed},
                        {"Package": "example/test", "Action": "fail"},
                    ], directory)
                    self.assertEqual(result.returncode, 1, result.stderr)
                    self.assertIn("TestOne", result.stdout)
                    self.assertNotIn("slowest", result.stdout)

    def test_parallel_subtest_is_visible_when_its_parent_has_no_elapsed_time(self):
        events = [
            {"Package": "example/test", "Test": "TestArchive/zip", "Action": "pass", "Elapsed": 4},
            {"Package": "example/test", "Test": "TestArchive/tar", "Action": "pass", "Elapsed": 3},
            {"Package": "example/test", "Test": "TestArchive", "Action": "pass", "Elapsed": 0},
            {"Package": "example/test", "Action": "pass", "Elapsed": 4},
        ]
        with tempfile.TemporaryDirectory() as directory:
            result = self.digest(events, directory)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("4.00s  example/test TestArchive/zip", result.stdout)
        self.assertNotIn("TestArchive/tar", result.stdout)


class VitestDigestTests(unittest.TestCase):
    def test_timing_summary_uses_assertion_milliseconds_and_ignores_skipped_tests(self):
        report = {"numTotalTests": 3, "numPassedTests": 2, "success": True, "testResults": [{
            "name": "/snapshot/lycaon-den/src/clock.test.ts", "status": "passed",
            "assertionResults": [
                {"fullName": "slow clock", "status": "passed", "duration": 2500},
                {"fullName": "fast clock", "status": "passed", "duration": 25},
                {"fullName": "skipped clock", "status": "pending", "duration": 10000},
            ],
        }]}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "report.json"
            path.write_text(json.dumps(report))
            result = subprocess.run([sys.executable, str(Path(__file__).resolve().parents[1].joinpath("vitest-digest.py")),
                                     "--input", str(path)], capture_output=True, text=True, timeout=5)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("2.50s  src/clock.test.ts slow clock", result.stdout)
        self.assertNotIn("fast clock", result.stdout)
        self.assertNotIn("skipped clock", result.stdout)

    def test_malformed_or_failed_report_cannot_pass_with_zero_process_exit(self):
        passed = {"numTotalTests": 1, "numPassedTests": 1, "testResults": [], "success": True}
        cases = [[], {}, {**passed, "success": False}, {**passed, "success": "false"}, {**passed, "testResults": [None]},
                 {**passed, "numPassedTests": 2}, {**passed, "numFailedTests": 1}]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "report.json"
            for report in [*cases, passed]:
                with self.subTest(report=report):
                    path.write_text(json.dumps(report))
                    result = subprocess.run([sys.executable, str(Path(__file__).resolve().parents[1].joinpath("vitest-digest.py")),
                                             "--input", str(path)], capture_output=True, text=True,
                                            env={**os.environ, "LAST_RUN_DIR": directory}, timeout=5)
                    self.assertEqual(result.returncode, 0 if report == passed else 1, result.stdout + result.stderr)
                    self.assertNotIn("Traceback", result.stderr)


class WireGeneratorTests(unittest.TestCase):
    def test_missing_local_generator_does_not_run_an_ancestor_installation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            script = root / "scripts/codegen-den-types.sh"
            script.parent.mkdir()
            script.write_bytes(Path(__file__).resolve().parents[1].joinpath("codegen-den-types.sh").read_bytes())
            schema = root / "docs/openapi.yaml"
            schema.parent.mkdir()
            schema.write_text("openapi: 3.1.0\n")
            ancestor = root / "node_modules/.bin/openapi-typescript"
            ancestor.parent.mkdir(parents=True)
            marker = root / "ancestor-ran"
            ancestor.write_text('#!/bin/sh\ntouch "$GENERATOR_MARKER"\n')
            ancestor.chmod(0o755)
            binary = root / "bin/node"
            binary.parent.mkdir()
            binary.write_text("#!/bin/sh\nprintf '1.0.0'\n")
            binary.chmod(0o755)
            result = subprocess.run(["bash", str(script), str(root / "out.ts")],
                                    env={**os.environ, "PATH": str(binary.parent) + os.pathsep + os.environ["PATH"],
                                         "GENERATOR_MARKER": str(marker)},
                                    capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 1)
            self.assertIn("openapi-typescript not found under lycaon-den/node_modules", result.stderr)
            self.assertFalse(marker.exists())


class WatchdogValidationTests(unittest.TestCase):
    def test_invalid_timeout_is_rejected_before_starting_a_watchdog(self):
        script = str(Path(__file__).resolve().parents[1].joinpath("process-group-watchdog.py"))
        for value in ("nan", "inf", "0", "-1", "invalid", ""):
            with self.subTest(value=value):
                result = subprocess.run([sys.executable, script, "--validate", value],
                                        capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode, 2)
                self.assertNotIn("Traceback", result.stderr)
        result = subprocess.run([sys.executable, script, "--validate", "0.5"],
                                capture_output=True, timeout=5)
        self.assertEqual(result.returncode, 0)


if __name__ == "__main__":
    unittest.main()

"""Bounded unittest selection from admission through the actual runner recipe."""

import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import unittest

import python_coverage
from toolchain import python_tools
import verification_plan as planning
import verification_resources as resources
from verification_tests import support

ROOT = Path(__file__).resolve().parents[3]


class UnittestSelectionTests(unittest.TestCase):
    def test_default_suite_and_resources_are_unchanged(self):
        stage = planning.request(["test:runner"], {})["stages"][0]
        self.assertEqual(stage["arguments"], ["test:runner"])
        self.assertEqual(resources.profile(stage, planning.catalog()), {"locks": ["runner"], "workers": 1})
        full = next(s for s in planning.expand(["check"]) if s["name"] == "test:runner")
        self.assertEqual(full, stage)

    def test_repeated_patterns_are_part_of_the_planned_stage_identity(self):
        arguments = ["test:runner", "--", "-k", "terminal", "-k", "*crash*"]
        stage = planning.request(arguments, {})["stages"][0]
        self.assertEqual(stage["arguments"], arguments)
        self.assertNotEqual(planning.stage_key(stage), planning.stage_key(planning.expand(["test:runner"])[0]))
        other = planning.request(["test:runner", "--", "-k", "other"], {})["stages"][0]
        self.assertNotEqual(planning.stage_key(stage), planning.stage_key(other))

    def test_unsupported_or_incomplete_selectors_are_refused_before_admission(self):
        for flags in (["-k"], ["-k", ""], ["-k", " "], ["-k", "-v"], ["-k=terminal"],
                      ["terminal"], ["-v"], ["--failfast"], ["-s", "/foreign"],
                      ["-k", "one", "two"], ["-k", "one", "-k"]):
            with self.subTest(flags=flags), self.assertRaisesRegex(ValueError, "No verification was queued"):
                planning.request(["test:runner", "--", *flags], {})
        with self.assertRaisesRegex(ValueError, "takes no selection arguments"):
            planning.validate_selection(["test:runner", "build"], ["-k", "terminal"])
        with self.assertRaisesRegex(ValueError, "one target at a time"):
            planning.validate_selection(["test:runner", "test:digest"], ["-k", "terminal"])

    def test_actual_unittest_or_selection_runs_only_matching_cases_and_default_stays_complete(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "test_fixture_execution.py").write_text(
                "import unittest\nclass Cases(unittest.TestCase):\n"
                " def test_alpha(self): pass\n def test_beta(self): pass\n"
                " def test_other(self): self.fail('unselected case executed')\n")
            command = [sys.executable, "-m", "unittest", "discover", "-s", directory,
                       "-p", "test_*execution.py"]
            selected = subprocess.run([*command, "-k", "alpha", "-k", "beta"],
                                      capture_output=True, text=True, timeout=20)
            self.assertEqual(selected.returncode, 0, selected.stdout + selected.stderr)
            self.assertIn("Ran 2 tests", selected.stderr)
            default = subprocess.run(command, capture_output=True, text=True, timeout=20)
            self.assertEqual(default.returncode, 1)
            self.assertIn("Ran 3 tests", default.stderr)
            self.assertIn("unselected case executed", default.stderr)

    def assert_actual_runner_recipe(self, flags, environment=None):
        result = subprocess.run([str(ROOT / "task"), "--dry", "--force", "test:runner",
                                 *(["--", *flags] if flags else [])],
                                cwd=ROOT, env=environment, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        lines = [line for line in (result.stdout + result.stderr).splitlines()
                 if line.startswith("task: [test:runner] ")]
        self.assertEqual(len(lines), 1,
                         f"expected one actual runner recipe; stdout={result.stdout!r}; "
                         f"stderr={result.stderr!r}")
        command = shlex.split(lines[0].partition("] ")[2])
        self.assertEqual(command, ["python3", "-m", "unittest", "discover", "-s", "scripts",
                                   "-p", "test_*execution.py", *flags])

    def test_actual_task_runner_recipe_forwards_patterns_without_changing_default(self):
        for flags in ([], ["-k", "*Terminal*", "-k", "crash receipt"]):
            with self.subTest(flags=flags):
                self.assert_actual_runner_recipe(flags)

    def test_actual_task_recipe_under_changed_coverage_exec_environment(self):
        with tempfile.TemporaryDirectory(prefix="runner-recipe-coverage-") as directory:
            tools = python_tools.prepared(python_coverage.REQUIREMENTS)
            python, data = python_coverage.environment(Path(directory), [tools], ["scripts/test-execution.py"])
            environment = {**os.environ, "PATH": f"{python.parent}{os.pathsep}{os.environ['PATH']}"}
            for flags in ([], ["-k", "*Terminal*", "-k", "crash receipt"]):
                with self.subTest(flags=flags):
                    self.assert_actual_runner_recipe(flags, environment)
            self.assertTrue(list(data.glob("started.*")), "actual task wrapper never started coverage")


class UnittestAdmissionTests(support.BatchFixture):
    def declare_runner(self):
        self.catalog["tasks"]["test:runner"] = ["test:runner"]
        self.catalog["selection"]["test:runner"] = "unittest"
        self.catalog["resources"]["test:runner"] = {"locks": ["runner"], "workers": 1}
        self.save_catalog()

    def test_invalid_selector_creates_no_request_or_execution(self):
        self.declare_runner()
        result = self.collect(self.start("test:runner", "--", "-k"), 2)
        self.assertIn("No verification was queued", result)
        self.assertEqual(self.queue.status()["runs"], [])
        self.assertEqual(self.records(), [])

    def test_admitted_runner_preserves_repeated_patterns_through_captured_execution(self):
        self.declare_runner()
        task = self.repo / "task"
        original = task.read_text()
        marker = "record={'name':name,"
        self.assertEqual(original.count(marker), 1)
        task.write_text(original.replace(marker,
            "record={'selection':args[args.index('--')+1:] if '--' in args else [],'name':name,"))
        flags = ["-k", "terminal", "-k", "*crash*"]
        request = self.start("test:runner", "--", *flags)
        self.queued(1)
        self.queue.resume()
        self.collect(request)
        self.assertEqual(self.records()[0]["selection"], flags)
        receipt = self.receipts()[0]
        self.assertEqual(receipt["status"], "passed")
        directory = next((self.artifacts / "verification").iterdir())
        stage = json.loads((directory / "resolved-plan.json").read_text())[0]["plan"]["stages"][0]
        self.assertEqual(stage["arguments"], ["test:runner", "--", *flags])

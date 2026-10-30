"""Changed scripts Python coverage, measured by real suites in a disposable repository."""

from contextlib import redirect_stdout
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import change_report
import coverage_changes
from toolchain import python_tools
import coverage_policy

ROOT = Path(__file__).resolve().parents[3]
BASE = """\
def covered():
    return 1


def legacy(value):
    first = value + 1
    second = first * 2
    third = second - 3
    fourth = third // 2
    fifth = fourth + 5
    sixth = fifth * 3
    return sixth
"""
ADDED = """

def added(value):
    doubled = value * 2
    tripled = doubled + value
    return tripled
"""


def git(root, *args):
    return subprocess.run(["git", "-C", str(root), *args], check=True, capture_output=True, text=True).stdout.strip()


class PythonChangedCoverageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="python-coverage-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "repo"
        self.scripts = self.root / "scripts"
        self.scripts.mkdir(parents=True)
        policy = Path(self.temp.name) / "coverage-policy.json"
        policy.write_text(json.dumps({"python": {"changed": {"percent": 70, "grace": 0}, "suites": {
            "fixture": {"start": "scripts", "pattern": "test_*execution.py"}}}}))
        (self.scripts / "sample.py").write_text(BASE)
        (self.scripts / "untouched.py").write_text("def unused():\n    return 1\n")
        self.write_test("self.assertEqual(sample.covered(), 1)")
        git(self.root, "init", "-q", "-b", "main")
        git(self.root, "config", "user.email", "test@example.com")
        git(self.root, "config", "user.name", "Test")
        git(self.root, "add", ".")
        git(self.root, "commit", "-q", "-m", "base")
        base = git(self.root, "rev-parse", "HEAD")
        for target in (patch.object(coverage_changes, "ROOT", self.root),
                       patch.object(coverage_changes, "artifact_root", return_value=Path(self.temp.name) / "artifacts"),
                       patch.object(coverage_policy, "POLICY", policy),
                       patch.dict(os.environ, {change_report.BASE_ENV: base})):
            target.start()
            self.addCleanup(target.stop)

    def write_test(self, *body):
        lines = "\n".join("        " + line for line in body)
        (self.scripts / "test_sample_execution.py").write_text(
            "import os\nfrom pathlib import Path\nimport shutil\nimport subprocess\nimport sys\n"
            "import unittest\n\nimport sample\n\n\nclass SampleTests(unittest.TestCase):\n"
            f"    def test_sample(self):\n{lines}\n")

    def run_gate(self):
        scope = change_report.resolve(self.root)
        findings, failed, _ = coverage_changes.run_python(scope)
        return findings, failed

    def test_an_untested_added_statement_fails_and_names_its_lines(self):
        (self.scripts / "sample.py").write_text(BASE + ADDED)
        findings, failed = self.run_gate()
        self.assertTrue(failed)
        errors = [f for f in findings if f.level == "error"]
        # Importing the module runs the new `def`; the body never runs.
        self.assertIn("sample.py: 1 of 4 changed statements covered (25%)", errors[0].text)
        self.assertEqual([(f.path, f.line, f.end_line) for f in errors[1:]], [("scripts/sample.py", 16, 18)])

    def test_a_tested_added_statement_passes(self):
        (self.scripts / "sample.py").write_text(BASE + ADDED)
        self.write_test("self.assertEqual(sample.covered(), 1)", "self.assertEqual(sample.added(2), 6)")
        out = io.StringIO()
        with patch.dict(os.environ, {"GITHUB_ACTIONS": ""}), redirect_stdout(out):
            self.assertEqual(coverage_changes.main(["python"]), 0)
        self.assertIn("Changed coverage (Python): change base", out.getvalue())
        self.assertIn("1 changed file(s)", out.getvalue())

    def test_statements_run_only_in_subprocesses_count(self):
        (self.scripts / "sample.py").write_text(BASE + ADDED + "\n\nif __name__ == '__main__':\n    added(1)\n")
        cases = {
            # A child with no environment at all, in isolated mode, still starts measurement.
            "cleared environment": ("subprocess.run([sys.executable, '-I', 'scripts/sample.py'], env={}, check=True)",),
            # A fixture that copies the script into a disposable tree is credited to the source.
            "copied script": ("copy = Path('..', 'copy', 'scripts').resolve()", "copy.mkdir(parents=True, exist_ok=True)",
                              "shutil.copy2('scripts/sample.py', copy / 'sample.py')",
                              "python = shutil.which('python3')",
                              "subprocess.run([python, str(copy / 'sample.py')], env={'PATH': os.environ['PATH']}, "
                              "cwd=copy, check=True)"),
        }
        for name, body in cases.items():
            with self.subTest(name):
                self.write_test(*body)
                findings, failed = self.run_gate()
                self.assertFalse(failed, [f.text for f in findings])
                self.assertEqual(findings, [])

    def test_deleted_and_unchanged_lines_do_not_count(self):
        # Deleting untested statements changes no statement.
        (self.scripts / "sample.py").write_text(BASE.replace("    third = second - 3\n", ""))
        self.assertEqual(self.run_gate(), ([], False))
        # legacy() stays untested; the change only deletes two of its statements and adds a comment.
        text = BASE.replace("    third = second - 3\n    fourth = third // 2\n", "    # third and fourth retired\n")
        (self.scripts / "sample.py").write_text(text.replace("fourth + 5", "second + 5"))
        lines = change_report.changed_lines(self.root, change_report.resolve(self.root).base, ["scripts/sample.py"])
        self.assertEqual(lines, {"scripts/sample.py": {8, 9}})
        findings, failed = self.run_gate()
        self.assertTrue(failed)
        # Only the edited assignment is a changed statement; the deleted lines, the comment, and the
        # untouched untested statements and file are not.
        self.assertIn("sample.py: 0 of 1 changed statements covered (0%)", findings[0].text)
        self.assertEqual([(f.path, f.line, f.end_line) for f in findings[1:]], [("scripts/sample.py", 9, 9)])
        self.assertFalse(any("untouched" in f.text or f.path == "scripts/untouched.py" for f in findings))

    def test_failing_tests_fail_without_measuring(self):
        (self.scripts / "sample.py").write_text(BASE + ADDED)
        self.write_test("self.assertEqual(sample.added(2), 0)")
        findings, failed = self.run_gate()
        self.assertTrue(failed)
        self.assertEqual([f.text for f in findings], ["tests failed, so changed coverage was not measured; fix the tests above"])

    def test_suites_that_never_started_coverage_fail_instead_of_reading_as_uncovered(self):
        (self.scripts / "sample.py").write_text(BASE + ADDED)
        self.write_test("self.assertEqual(sample.added(2), 6)")
        empty = Path(self.temp.name) / "no-tools"
        empty.mkdir()
        with patch.object(python_tools, "provision", return_value=empty):
            findings, failed = self.run_gate()
        self.assertTrue(failed)
        self.assertEqual([f.text for f in findings],
                         ["sample.py: statements could not be read: coverage.py did not start in any measured suite process"])

    def test_test_code_and_files_outside_scripts_are_not_units(self):
        paths = ["scripts/sample.py", "scripts/test_sample_execution.py", "scripts/verification_tests/support.py",
                 "lycaon/tool.py", "scripts/notes.md", "scripts/gone.py"]
        (self.scripts / "verification_tests").mkdir()
        (self.scripts / "verification_tests" / "support.py").write_text("")
        (self.scripts / "notes.md").write_text("")
        self.assertEqual(coverage_changes.python_coverage.sources(self.root, paths), ["scripts/sample.py"])


class ProvisionTests(unittest.TestCase):
    def test_each_requirements_file_installs_once_per_interpreter(self):
        with tempfile.TemporaryDirectory() as directory:
            requirements = Path(directory) / "requirements.txt"
            requirements.write_text("example==1.0\n")
            calls = []

            def install(command, check):
                calls.append(command)
                Path(command[command.index("--target") + 1]).mkdir()

            with patch.dict(os.environ, {"PW_BIN_DIR": str(Path(directory) / "bin")}), \
                    patch.object(python_tools.subprocess, "run", side_effect=install):
                first = python_tools.provision(requirements)
                second = python_tools.provision(requirements)
            self.assertEqual(first, second)
            self.assertEqual(len(calls), 1)
            self.assertEqual(calls[0][-2:], ["-r", str(requirements)])
            self.assertIn("--no-deps", calls[0])
            self.assertEqual((first / "ready").read_text(), "example==1.0\n")

    def test_prepared_tools_require_a_completed_matching_pin(self):
        with tempfile.TemporaryDirectory() as temporary:
            requirements = Path(temporary) / "requirements.txt"
            requirements.write_text("example==1.0\n")
            with patch.dict(os.environ, {"PW_BIN_DIR": str(Path(temporary) / "bin")}):
                with self.assertRaisesRegex(ValueError, "setup-dev preparation"):
                    python_tools.prepared(requirements)
                target = python_tools.directory(requirements)
                target.mkdir(parents=True)
                (target / "ready").write_text(requirements.read_text())
                self.assertEqual(python_tools.prepared(requirements), target)
                requirements.write_text("example==2.0\n")
                with self.assertRaises(ValueError):
                    python_tools.prepared(requirements)

    def test_provisioning_installs_the_pin_and_every_suite_requirement(self):
        with patch.object(python_tools, "provision") as provision:
            self.assertEqual(coverage_changes.main(["python", "--provision"]), 0)
        installed = [call.args[0] for call in provision.call_args_list]
        self.assertEqual(installed, [coverage_changes.python_coverage.REQUIREMENTS,
                                     ROOT / "scripts/coordinator-benchmark/requirements.txt",
                                     ROOT / "scripts/bialy/custom_model/requirements-test.txt"])
        with patch("sys.stderr"), self.assertRaises(SystemExit):
            coverage_changes.main(["go", "--provision"])


class DeclaredSuiteTests(unittest.TestCase):
    def test_runner_suite_is_the_test_runner_discovery(self):
        suite = coverage_policy.python_suites()["test:runner"]
        body = re.search(r"^  test:runner:\n(?:    .*\n)+", (ROOT / "Taskfile.yml").read_text(), re.M).group(0)
        self.assertIn(f"python3 -m unittest discover -s {suite['start']} -p '{suite['pattern']}'", body)

    def test_suites_need_a_start_and_pattern(self):
        with tempfile.TemporaryDirectory() as directory:
            policy = Path(directory) / "policy.json"
            for suites in ({}, {"x": {"start": "scripts"}}, {"x": {"start": "scripts", "pattern": "", "other": "y"}}):
                policy.write_text(json.dumps({"python": {"suites": suites}}))
                with self.subTest(suites=suites), patch.object(coverage_policy, "POLICY", policy), \
                        self.assertRaises(ValueError):
                    coverage_policy.python_suites()


if __name__ == "__main__":
    unittest.main()

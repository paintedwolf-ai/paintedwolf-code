"""A nightly run binds the one commit and tree every stage verifies."""
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import nightly_source as source

SOURCE = "a" * 40
CONTROLLER = "b" * 40
TREE = "c" * 40


class NightlySourceTests(unittest.TestCase):
    def select(self, environment, tooling=0):
        with patch.object(source, "commit", side_effect=lambda value: value), \
                patch.object(source.subprocess, "check_output", return_value=TREE + "\n") as git, \
                patch("ci_policy.qualification_source.subprocess.run") as diff:
            diff.return_value.returncode = tooling
            return source.select(environment), git

    def test_scheduled_run_verifies_its_own_commit(self):
        record, git = self.select({"GITHUB_SHA": CONTROLLER, "NIGHTLY_SOURCE": ""})
        self.assertEqual(record, {"sha": CONTROLLER, "tree": TREE})
        git.assert_called_once_with(["git", "rev-parse", CONTROLLER + "^{tree}"], cwd=source.ROOT, text=True)

    def test_dispatch_names_another_commit_with_the_same_tooling(self):
        record, _ = self.select({"GITHUB_SHA": CONTROLLER, "NIGHTLY_SOURCE": SOURCE})
        self.assertEqual(record, {"sha": SOURCE, "tree": TREE})
        with self.assertRaisesRegex(ValueError, "different CI tooling"):
            self.select({"GITHUB_SHA": CONTROLLER, "NIGHTLY_SOURCE": SOURCE}, tooling=1)

    def test_command_reports_the_binding_or_refuses_an_inexact_source(self):
        with tempfile.TemporaryDirectory() as directory:
            output, summary = Path(directory) / "output", Path(directory) / "summary"
            environment = {"GITHUB_OUTPUT": str(output), "GITHUB_STEP_SUMMARY": str(summary)}
            with patch.dict(source.os.environ, environment), \
                    patch.object(source, "select", return_value={"sha": SOURCE, "tree": TREE}):
                self.assertEqual(source.main(), 0)
            self.assertEqual(dict(line.split("=", 1) for line in output.read_text().splitlines()),
                             {"sha": SOURCE, "tree": TREE})
            self.assertIn(SOURCE, summary.read_text())
            for refusal in (ValueError("qualification source must be a full commit SHA"),
                            subprocess.CalledProcessError(128, ["git"])):
                with patch.dict(source.os.environ, environment), patch.object(source, "select", side_effect=refusal), \
                        patch("builtins.print") as said:
                    self.assertEqual(source.main(), 1)
                self.assertIn("::error title=Nightly source::", said.call_args.args[0])
            self.assertEqual(len(output.read_text().splitlines()), 2)


if __name__ == "__main__":
    unittest.main()

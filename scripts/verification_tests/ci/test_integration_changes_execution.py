"""The integration tests of directly edited Go packages run at the pull request."""
import os
import subprocess
import unittest
from unittest.mock import patch

import change_report
import integration_changes
from verification_plan import catalog

DECLARED = catalog()["go"]["test:integration"]["packages"]


class IntegrationChangesTests(unittest.TestCase):
    def test_a_changed_file_selects_the_package_that_holds_it(self):
        self.assertEqual(integration_changes.edited_packages([
            "lycaon/internal/parse/impl.go",
            "lycaon/internal/parse/fuzz_test.go",
            "lycaon/test/integration/git_parity_test.go",
            "lycaon/internal/tools/native/survey/summarize_integration_test.go",
            "lycaon/internal/tools/native/extract_archive_capacity_integration_test.go",
        ], DECLARED), ["./internal/parse", "./internal/tools/native", "./internal/tools/native/survey", "./test/integration"])

    def test_testdata_belongs_to_the_package_above_it(self):
        self.assertEqual(integration_changes.edited_packages(
            ["lycaon/internal/parse/testdata/fuzz/FuzzValidate/seed"], DECLARED), ["./internal/parse"])

    def test_paths_the_recipe_does_not_cover_select_nothing(self):
        for path in ("lycaon/cmd/arch-lint/main.go", "lycaon-den/src/app.ts", "lycaon/go.mod",
                     "lycaon/internal/removed/gone.go", "docs/README.md",
                     "lycaon/internal/decide/native/src/lib.rs",
                     "lycaon/internal/generation/native/src/lib.rs",
                     "lycaon/internal/documentcore/native/src/lib.rs",
                     "lycaon/test/fixtures/e2e/minimal-go-project/internal/app/app.go"):
            with self.subTest(path=path):
                self.assertEqual(integration_changes.edited_packages([path], DECLARED), [])

    def test_host_packages_drops_only_foreign_platform_packages(self):
        self.assertEqual(integration_changes.host_packages([], ["-tags=integration"]), [])
        completed = subprocess.CompletedProcess(
            args=[], returncode=0,
            stdout=(
                "/repo/internal/parse\t\n"
                "/repo/internal/layeredfs/adapter/nfsdarwin\tbuild constraints exclude all Go files in /repo/nfsdarwin\n"
                "/repo/internal/broken\tsyntax error\n"
            ),
        )
        with patch.object(integration_changes.subprocess, "run", return_value=completed) as run:
            kept = integration_changes.host_packages(
                ["./internal/parse", "./internal/layeredfs/adapter/nfsdarwin", "./internal/broken"],
                ["-tags=integration", "-count=1"],
            )
        self.assertEqual(kept, ["./internal/parse", "./internal/broken"])
        self.assertIn("-tags=integration", run.call_args.args[0])
        self.assertNotIn("-count=1", run.call_args.args[0])

    def test_edited_packages_run_through_the_integration_recipe(self):
        scope = change_report.Scope("base", "test", ["lycaon/internal/parse/impl.go", "docs/README.md"])
        with patch.object(integration_changes.change_report, "resolve", return_value=scope), \
                patch.object(integration_changes, "host_packages", side_effect=lambda pkgs, _flags: pkgs), \
                patch.object(integration_changes.os, "execv") as execute:
            integration_changes.main()
        command = execute.call_args.args[1]
        self.assertEqual(command[2:], ["plan", "--", "test:integration", "--", "./internal/parse"])
        self.assertTrue(command[1].endswith("/scripts/test-execution.py"))

    def test_a_change_without_edited_packages_runs_nothing(self):
        scope = change_report.Scope("base", "test", ["lycaon-den/src/app.ts"])
        with patch.object(integration_changes.change_report, "resolve", return_value=scope), \
                patch.object(integration_changes.os, "execv") as execute:
            self.assertEqual(integration_changes.main(), 0)
        execute.assert_not_called()

    def test_no_change_base_is_unverified_only_in_ci(self):
        for actions, code in (("true", 2), ("", 0)):
            with self.subTest(actions=actions), \
                    patch.object(integration_changes.change_report, "resolve", return_value=None), \
                    patch.dict(os.environ, {"GITHUB_ACTIONS": actions}), \
                    patch.object(integration_changes.os, "execv") as execute:
                self.assertEqual(integration_changes.main(), code)
                execute.assert_not_called()


if __name__ == "__main__":
    unittest.main()

"""Workflow commands preserve machine-readable plans, required-job verdicts and gate errors."""
import contextlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import yaml
from verification_tests.ci.fixture_plan import live

DEN = live(runs="den:typecheck")
ADMISSION = live(phase="admission")
ADMISSION_TESTS = live(phase="admission-tests")
LINT_ADMISSION = live(runs="arch-lint:changes", area="always")

import ci_verification


ROOT = Path(__file__).resolve().parents[3]


class WorkflowCommandTests(unittest.TestCase):
    def test_release_packages_the_prepared_application_and_preserves_failures(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/release.yml').read_text())
        steps = workflow['jobs']['build-release']['steps']
        package = next(step for step in steps if step.get('name') == 'Sign, notarize, staple and package the compiled application')
        for task_exit in (0, 37):
            with self.subTest(task_exit=task_exit), tempfile.TemporaryDirectory() as directory:
                scratch = Path(directory)
                record = scratch / 'task-arguments'
                task = scratch / 'task'
                task.write_text('#!/bin/sh\nprintf "%s\\n" "$@" > "$RECORD"\nexit "$TASK_EXIT"\n')
                task.chmod(0o700)
                notary = scratch / '.release-controller/scripts/macos-notary-tools/notary.py'
                notary.parent.mkdir(parents=True)
                notary.write_text('')
                environment = {**os.environ, 'GITHUB_WORKSPACE': str(scratch), 'RUNNER_TEMP': str(scratch),
                               'RECORD': str(record), 'TASK_EXIT': str(task_exit)}
                for name in ('TAURI_SIGNING_PRIVATE_KEY', 'APPLE_API_ISSUER', 'APPLE_API_KEY'):
                    environment[name] = 'fixture'
                result = subprocess.run(['bash', '-c', package['run']], cwd=scratch, env=environment,
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, task_exit, result.stderr)
                self.assertEqual(record.read_text().splitlines(),
                                 ['den:bundle', '--', '--phase', 'package', '--archive', f'{scratch}/prepared/compiled.tar'])

    def command(self, *arguments, **environment):
        return subprocess.run([sys.executable, 'scripts/ci_verification.py', *arguments],
                              cwd=ROOT, env={**os.environ, **environment},
                              capture_output=True, text=True)

    def test_a_qualified_source_reports_the_preparation_qualification_checked(self):
        run = {'id': 7, 'receipt': {'preparation_run_id': 41}}
        output = io.StringIO()
        with patch.object(ci_verification.verification_verdict, 'require_qualification', return_value=run) as require, \
                patch.object(sys, 'argv', ['ci_verification.py', 'verified', 'a' * 40]), \
                patch.dict(os.environ, {'GITHUB_REPOSITORY': 'owner/repo'}), contextlib.redirect_stdout(output):
            ci_verification.main()
        require.assert_called_once_with('owner/repo', 'a' * 40)
        self.assertEqual(output.getvalue(), 'preparation_run_id=41\n')

    def test_required_job_verdict_reaches_the_process_exit(self):
        results = json.dumps({'verification': {'result': 'success'}, 'platform': {'result': 'skipped'}})
        passed = self.command('gate', '--skipped', 'platform', NEEDS_JSON=results)
        self.assertEqual(passed.returncode, 0, passed.stderr)
        draft = self.command('gate', '--skipped', 'platform', '--draft', 'true', NEEDS_JSON=results)
        self.assertNotEqual(draft.returncode, 0)
        self.assertIn('draft pull requests are not verified', draft.stderr)

    def test_schedule_summary_preserves_matrix_json_output(self):
        with tempfile.TemporaryDirectory() as directory:
            summary = Path(directory) / "summary.md"
            measured = Path(directory) / "timings.json"
            measured.write_text(json.dumps({"run": 42, "lanes": {ADMISSION: 250, LINT_ADMISSION: 200, DEN: 230,
                                                         **{f"{ADMISSION_TESTS} {index}/4": 240 for index in range(1, 5)}}}))
            result = self.command('matrix', 'fast', '--timings', str(measured),
                                  GITHUB_STEP_SUMMARY=str(summary))
            self.assertEqual(result.returncode, 0, result.stderr)
            rows = json.loads(result.stdout)['include']
            self.assertEqual(rows[0]['timing_basis'], 'measured')
            self.assertEqual(rows[0]['expected_seconds'], 250)
            self.assertIn('Estimate basis: `measured`', summary.read_text())
            self.assertIn('Nightly timing source: run 42.', summary.read_text())


class PlatformEvidenceTests(unittest.TestCase):
    def test_linux_confinement_legs_execute_once_before_and_after_the_build(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/platform-verification.yml").read_text())
        steps = {step.get("id"): step for step in workflow["jobs"]["upgrade-corpus"]["steps"]}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            task = root / "task"
            task.write_text("#!/bin/sh\nprintf '%s\\n' \"$@\"\n")
            task.chmod(0o755)
            legs = []
            for name in ("launch", "confinement"):
                result = subprocess.run(["bash", "-e", "-c", steps[name]["run"]], cwd=root,
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                arguments = result.stdout.splitlines()
                self.assertEqual(arguments[:2], ["test:confine:linux", "--"])
                legs.extend(arguments[2:])
            self.assertEqual(legs, ["launch", "sandbox", "supervisor", "browser", "helpers"])
            self.assertEqual(steps["build"]["if"], "${{ !cancelled() }}")
            self.assertEqual(steps["corpus"]["if"], "${{ !cancelled() && steps.build.outcome == 'success' }}")

    def test_upgrade_output_survives_success_and_failure(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/platform-verification.yml").read_text())
        step = next(item for item in workflow["jobs"]["upgrade-corpus"]["steps"]
                    if item.get("name") == "Boot upgrade corpus fixtures")
        for code in (0, 23):
            with self.subTest(code=code), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                (root / "scripts").mkdir()
                shutil.copyfile(ROOT / "scripts/artifact_paths.py", root / "scripts/artifact_paths.py")
                task = root / "task"
                task.write_text("#!/bin/sh\n[ \"$#\" -eq 1 ] && [ \"$1\" = upgrade:corpus:boot ] || exit 98\n"
                                f"echo fixture-start\necho fixture-diagnostic >&2\nexit {code}\n")
                task.chmod(0o755)
                artifacts = root / "evidence"
                environment = {**os.environ, "GITHUB_WORKSPACE": str(root),
                               "PW_TEST_ARTIFACT_ROOT": str(artifacts)}
                result = subprocess.run(["bash", "-e", "-c", step["run"]], cwd=root, env=environment,
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, code, result.stderr)
                expected = "fixture-start\nfixture-diagnostic\n"
                self.assertEqual(result.stdout, expected)
                self.assertEqual((artifacts / "ci/upgrade-corpus.log").read_text(), expected)

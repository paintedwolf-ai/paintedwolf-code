"""Workflow commands preserve machine-readable plans and required-job verdicts."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import yaml


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
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, task_exit, result.stderr)
                self.assertEqual(record.read_text().splitlines(),
                                 ['den:bundle', '--', '--phase', 'package', '--archive', f'{scratch}/prepared/compiled.tar'])

    def command(self, *arguments, **environment):
        return subprocess.run([sys.executable, 'scripts/ci_verification.py', *arguments],
                              cwd=ROOT, env={**os.environ, **environment},
                              capture_output=True, text=True, timeout=30)

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
            measured.write_text(json.dumps({"run": 42, "lanes": {"ready": 250, "ready-lint": 200, "frontend": 230,
                                                         **{f"ready-tests {index}/4": 240 for index in range(1, 5)}}}))
            result = self.command('matrix', 'fast', '--timings', str(measured),
                                  GITHUB_STEP_SUMMARY=str(summary))
            self.assertEqual(result.returncode, 0, result.stderr)
            rows = json.loads(result.stdout)['include']
            self.assertEqual(rows[0]['timing_basis'], 'measured')
            self.assertEqual(rows[0]['expected_seconds'], 250)
            self.assertIn('Estimate basis: `measured`', summary.read_text())
            self.assertIn('Nightly timing source: run 42.', summary.read_text())

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
    def test_release_builds_declared_package_families_and_preserves_failures(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/release.yml').read_text())
        steps = workflow['jobs']['build-release']['steps']
        build = next(step for step in steps if step.get('name') == 'Build, sign, and notarize')
        self.assertEqual(build['env']['PACKAGE_EXTENSIONS'], "${{ join(matrix.package_extensions, ',') }}")
        dependencies = next(step for step in steps if step.get('name') == 'Install Linux bundle dependencies')
        self.assertIn('libarchive-tools', dependencies['run'].split())
        catalog = json.loads((ROOT / 'packaging/release-platforms.json').read_text())
        cases = [(row['goos'], ','.join(row['package_extensions']), 0) for row in catalog['platforms']]
        cases += [('linux', 'deb,rpm', 0), ('linux', 'deb', 0), ('linux', 'deb,rpm,pkg.tar.zst', 37)]
        for goos, extensions, task_exit in cases:
            with self.subTest(goos=goos, extensions=extensions, task_exit=task_exit), tempfile.TemporaryDirectory() as directory:
                scratch = Path(directory)
                record = scratch / 'task-arguments'
                task = scratch / 'task'
                task.write_text('#!/bin/sh\nprintf "%s\\n" "$@" > "$RECORD"\nexit "$TASK_EXIT"\n')
                task.chmod(0o700)
                notary = scratch / '.release-controller/scripts/macos-notary-tools/notary.py'
                notary.parent.mkdir(parents=True)
                notary.write_text('')
                environment = {**os.environ, 'GOOS': goos, 'PACKAGE_EXTENSIONS': extensions,
                               'GITHUB_WORKSPACE': str(scratch), 'RUNNER_TEMP': str(scratch),
                               'RECORD': str(record), 'TASK_EXIT': str(task_exit)}
                for name in ('TAURI_SIGNING_PRIVATE_KEY', 'APPLE_SIGNING_IDENTITY', 'APPLE_API_ISSUER',
                             'APPLE_API_KEY', 'AZURE_CLIENT_ID', 'AZURE_CLIENT_SECRET', 'AZURE_TENANT_ID',
                             'AZURE_ARTIFACT_SIGNING_ENDPOINT', 'AZURE_ARTIFACT_SIGNING_ACCOUNT',
                             'AZURE_ARTIFACT_SIGNING_PROFILE'):
                    environment[name] = 'fixture'
                result = subprocess.run(['bash', '-c', build['run']], cwd=scratch, env=environment,
                                        capture_output=True, text=True, timeout=10)
                if goos == 'linux' and extensions not in {'deb,rpm', 'deb,rpm,pkg.tar.zst'}:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(record.exists(), 'unsupported package forms must not start a build')
                else:
                    self.assertEqual(result.returncode, task_exit, result.stderr)
                    expected = ['den:bundle']
                    if goos == 'linux' and extensions == 'deb,rpm,pkg.tar.zst':
                        expected += ['--', '--pacman-container']
                    self.assertEqual(record.read_text().splitlines(), expected)

    def command(self, *arguments, **environment):
        return subprocess.run([sys.executable, 'scripts/ci_verification.py', *arguments],
                              cwd=ROOT, env={**os.environ, **environment},
                              capture_output=True, text=True, timeout=30)

    def test_ready_plan_and_capacity_are_workflow_outputs(self):
        matrix = self.command('matrix', 'fast')
        self.assertEqual(matrix.returncode, 0, matrix.stderr)
        rows = json.loads(matrix.stdout)['include']
        self.assertEqual([row['lane'] for row in rows], ['ready'])
        capacity = self.command('max-parallel', 'fast')
        self.assertEqual(capacity.returncode, 0, capacity.stderr)
        self.assertEqual(int(capacity.stdout), 1)

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
            measured.write_text(json.dumps({"runs": [42], "lanes": {"ready": 250}}))
            result = self.command('matrix', 'fast', '--timings', str(measured),
                                  GITHUB_STEP_SUMMARY=str(summary))
            self.assertEqual(result.returncode, 0, result.stderr)
            rows = json.loads(result.stdout)['include']
            self.assertEqual(rows[0]['timing_basis'], 'measured')
            self.assertEqual(rows[0]['expected_seconds'], 250)
            self.assertIn('Estimate basis: `measured`', summary.read_text())
            self.assertIn('Nightly timing sources: 42', summary.read_text())

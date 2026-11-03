"""Disposable runner cleanup preserves selected tools and non-hosted machines."""
from types import SimpleNamespace
import json
import os
import sys
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

from ci_policy import runner_storage


class RunnerStorageTests(unittest.TestCase):
    environment = {'RUNNER_ENVIRONMENT': 'github-hosted', 'RUNNER_OS': 'Linux',
                   'ImageOS': 'ubuntu24', 'GITHUB_WORKSPACE': '/workspace'}

    def test_local_self_hosted_and_other_images_never_inspect_or_remove_storage(self):
        for override in ({'RUNNER_ENVIRONMENT': ''}, {'RUNNER_ENVIRONMENT': 'self-hosted'},
                         {'RUNNER_OS': 'macOS'}, {'RUNNER_OS': 'Windows'}, {'ImageOS': 'ubuntu26'}):
            with self.subTest(override=override), patch.object(runner_storage.shutil, 'disk_usage') as usage, \
                    patch.object(runner_storage.subprocess, 'run') as run:
                result = runner_storage.prepare({**self.environment, **override})
                self.assertEqual(result['state'], 'unchanged')
                usage.assert_not_called()
                run.assert_not_called()

    def test_low_space_removes_only_unused_sdks_and_reports_actual_recovery(self):
        before, after = 14 * runner_storage.GIB, 29 * runner_storage.GIB
        usage = [SimpleNamespace(free=before),
                 SimpleNamespace(free=after)]
        with patch.object(runner_storage.shutil, 'disk_usage', side_effect=usage), \
                patch.object(Path, 'exists', return_value=True), \
                patch.object(runner_storage.subprocess, 'run') as run:
            result = runner_storage.prepare(self.environment)
        run.assert_called_once_with(['sudo', '-n', 'rm', '-rf', '--',
                                     '/usr/local/lib/android', '/usr/share/dotnet'], check=True)
        self.assertEqual(result['free_before_bytes'], before)
        self.assertEqual(result['free_after_bytes'], after)
        self.assertEqual(result['reclaimed_bytes'], after - before)

    def test_sufficient_space_and_absent_sdks_need_no_deletion(self):
        for free, exists in ((40 * runner_storage.GIB, True), (14 * runner_storage.GIB, False)):
            with self.subTest(free=free, exists=exists), \
                    patch.object(runner_storage.shutil, 'disk_usage', return_value=
                                 SimpleNamespace(free=free)), \
                    patch.object(Path, 'exists', return_value=exists), \
                    patch.object(runner_storage.subprocess, 'run') as run:
                result = runner_storage.prepare(self.environment)
                run.assert_not_called()
                self.assertEqual(result['reclaimed_bytes'], 0)
                self.assertEqual(result['removed_unused_sdks'], [])

    def test_removal_failure_is_not_reported_as_reclaimed_space(self):
        with patch.object(runner_storage.shutil, 'disk_usage', return_value=
                          SimpleNamespace(free=14 * runner_storage.GIB)), \
                patch.object(Path, 'exists', return_value=True), \
                patch.object(runner_storage.subprocess, 'run', side_effect=
                             subprocess.CalledProcessError(1, ['sudo'])):
            with self.assertRaises(subprocess.CalledProcessError):
                runner_storage.prepare(self.environment)

    def test_setup_reclaims_storage_before_toolchains_and_managed_work(self):
        import yaml
        root = Path(__file__).resolve().parents[3]
        steps = yaml.safe_load((root / '.github/actions/setup-verification/action.yml').read_text())['runs']['steps']
        index = next(i for i, step in enumerate(steps) if step.get('run') ==
                     'PYTHONPATH=scripts python3 -m ci_policy.runner_storage')
        self.assertEqual(index, 0)
        self.assertIn("inputs.lookup-only != 'true'", steps[index]['if'])

    def test_cli_on_a_self_hosted_environment_is_read_only(self):
        root = Path(__file__).resolve().parents[3]
        result = subprocess.run([sys.executable, '-m', 'ci_policy.runner_storage'], cwd=root,
                                env={**os.environ, 'PYTHONPATH': str(root / 'scripts'),
                                     'RUNNER_ENVIRONMENT': 'self-hosted'},
                                capture_output=True, text=True, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)['state'], 'unchanged')

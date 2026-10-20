"""Contracts for real installed Nix operations; no mocked installed pass claims."""
from __future__ import annotations

import copy
import errno
import hashlib
import subprocess
import tempfile
from pathlib import Path
import unittest
from unittest import mock

from scripts.linux_release.installed_cases import nix_workflows as nix
from scripts.linux_release.installed_cases.records import CaseUnavailable


class InstalledNixWorkflowsTests(unittest.TestCase):
    def test_daemon_authority_requires_kernel_denial_and_actual_approved_ping(self):
        ordinary = {'socket_errno': errno.EACCES, 'exit_code': 1, 'pid_namespace': 22}
        approved = {'socket_errno': 0, 'exit_code': 0, 'pid_namespace': 11,
                    'stdout': '{"version":"2.35.3"}'}
        self.assertTrue(nix.daemon_facts(ordinary, approved, 11))
        for which, field, value in [
            ('ordinary', 'socket_errno', errno.ENOENT),
            ('ordinary', 'socket_errno', 0), ('ordinary', 'exit_code', 0),
            ('ordinary', 'pid_namespace', 11), ('approved', 'socket_errno', errno.EACCES),
            ('approved', 'exit_code', 1), ('approved', 'pid_namespace', 22),
            ('approved', 'stdout', 'success'), ('approved', 'stdout', '{}')]:
            with self.subTest(which=which, field=field, value=value):
                first, second = copy.deepcopy(ordinary), copy.deepcopy(approved)
                (first if which == 'ordinary' else second)[field] = value
                self.assertFalse(nix.daemon_facts(first, second, 11))

    def test_source_uses_committed_lock_and_full_selected_revision(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            lock = b'{"nodes":{}}\n'
            source = 'a' * 40
            binding = {'source_commit': source,
                       'case_inputs': {'nix': {'format_version': 1, 'source_root': str(root)}},
                       'artifact': {'identity': {'lock_sha256': hashlib.sha256(lock).hexdigest()}}}
            result = subprocess.CompletedProcess([], 0, lock, b'')
            with mock.patch.object(nix.subprocess, 'run', return_value=result) as run:
                target = nix.source_target(binding)
                self.assertEqual(target, 'git+' + root.as_uri() + '?rev=' + source)
                self.assertIn(source + ':flake.lock', run.call_args.args[0])
                self.assertNotIn('HEAD:flake.lock', run.call_args.args[0])
            for output in [b'changed', b'x' * ((1 << 20) + 1)]:
                with mock.patch.object(nix.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, output, b'')):
                    with self.assertRaises(ValueError):
                        nix.source_target(binding)
            binding['case_inputs']['nix']['command'] = 'uncontrolled hook'
            with self.assertRaises(CaseUnavailable):
                nix.source_target(binding)

    def test_missing_source_is_unverified_and_invalid_revisions_are_refused(self):
        with self.assertRaises(CaseUnavailable):
            nix.source_target({})
        with tempfile.TemporaryDirectory() as directory:
            binding = {'source_commit': 'HEAD', 'case_inputs': {'nix': {
                'format_version': 1, 'source_root': str(Path(directory).resolve())}}}
            with self.assertRaises(ValueError):
                nix.source_target(binding)

    def test_fixed_probe_script_is_valid_and_does_not_enable_remote_authority(self):
        compile(nix.SCRIPT, '<installed-nix-workflow>', 'exec')
        self.assertIn("'--offline'", nix.SCRIPT)
        self.assertIn("'builders', ''", nix.SCRIPT)
        self.assertIn("'extra-substituters', ''", nix.SCRIPT)
        self.assertNotIn('sudo', nix.SCRIPT)
        self.assertNotIn('trusted-public-keys', nix.SCRIPT)
        self.assertNotIn('require-sigs', nix.SCRIPT)

    def test_unsupported_cases_retain_unverified_without_starting_app(self):
        binding = {'source_commit': 'a' * 40, 'artifact': {'id': 'nix-artifact'}}
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(nix, 'InstalledApp') as app:
            result = nix.execute(binding, 'updates', 'running-app-upgrade', Path(directory))
            self.assertEqual(result['status'], 'unverified')
            self.assertTrue(result['evidence'])
            app.assert_not_called()


if __name__ == '__main__':
    unittest.main()

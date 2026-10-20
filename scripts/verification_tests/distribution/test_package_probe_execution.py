"""Temporary package diagnostics retain exact profile ownership and cleanup on failure."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch
from scripts.linux_release import package_probe as owner


class PackageProbeTests(unittest.TestCase):
    def test_profile_has_one_literal_engine_attachment(self):
        executable = '/tmp/owned extraction/deb/opt/painted-wolf-code/bin/pw'
        name, source = owner.profile_source(executable)
        self.assertIn(('"' + executable + '"').encode(), source)
        self.assertIn(b'userns,', source)
        self.assertNotIn(b'mount,', source)
        self.assertTrue(name.startswith('paintedwolf-package-probe-'))
        for token in ('*', '?', '[', ']', '{', '}', '"', '\\', '\n'):
            with self.subTest(token=token), self.assertRaises(ValueError):
                owner.profile_source(executable.replace('owned', 'owned' + token))

    def test_physical_identity_refuses_group_write_and_symlinks(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary).resolve() / 'engine'
            path.write_bytes(b'fixture'); path.chmod(0o700)
            measured = owner.physical(path, owner=os.geteuid(), executable=True)
            self.assertEqual(measured['sha256'], hashlib.sha256(b'fixture').hexdigest())
            link = path.parent / 'link'; link.symlink_to(path)
            with self.assertRaises(ValueError):
                owner.physical(link, owner=os.geteuid())
            path.chmod(0o720)
            with self.assertRaises(ValueError):
                owner.physical(path, owner=os.geteuid())

    def test_fixed_probe_dispatch_requires_no_publishing_toolchain(self):
        root = Path(__file__).resolve().parents[3]
        with tempfile.TemporaryDirectory() as temporary:
            tools = Path(temporary)
            (tools / 'python3').symlink_to(sys.executable)
            (tools / 'dirname').symlink_to('/usr/bin/dirname')
            (tools / 'env').symlink_to('/usr/bin/env')
            result = subprocess.run(['/bin/bash', str(root / 'scripts/release-r2-live-test.sh'),
                                     '--local-package-probe', '--help'],
                                    env={**os.environ, 'PATH': str(tools)}, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.assertIn(b'--require-baseline-refusal', result.stdout)

    @unittest.skipUnless(hasattr(os, 'waitid') and hasattr(os, 'WNOWAIT'), 'requires Linux waitable-child identity')
    def test_diagnostic_cleans_group_before_reaping_its_owned_leader(self):
        original = subprocess.Popen
        events = []
        def child(*arguments, **keywords):
            process = original(*arguments, **keywords)
            actual_wait = process.wait
            def wait(*args, **kwargs):
                events.append('reap')
                self.assertIn('group-cleanup', events)
                return actual_wait(*args, **kwargs)
            process.wait = wait
            return process
        actual_kill = os.killpg
        def cleanup(pid, signal):
            events.append('group-cleanup')
            return actual_kill(pid, signal)
        with patch.object(owner.subprocess, 'Popen', side_effect=child), patch.object(owner.os, 'killpg', side_effect=cleanup):
            result = owner.diagnostic([sys.executable, '-c', 'print("owned child")'], os.environ.copy())
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, b'owned child\n')
        self.assertLess(events.index('group-cleanup'), events.index('reap'))

    def fixture(self):
        binding = {'tree': '/tmp/package', 'pw': {'path': '/tmp/package/opt/painted-wolf-code/bin/pw',
                   'sha256': 'a' * 64, 'bytes': 100},
                   'pw-document-core': {'path': '/tmp/package/opt/painted-wolf-code/bin/pw-document-core'}}
        name, _ = owner.profile_source(binding['pw']['path'])
        policy = Mock(); policy.exists.return_value = True; policy.read_text.return_value = '1\n'
        commands = []
        def execute(arguments, **kwargs):
            commands.append(arguments)
            if arguments[:3] == ['sudo', '-n', 'cat']:
                removed = any(command[-1] == '--remove' for command in commands)
                added = any(command[-1] == '--add' for command in commands)
                return subprocess.CompletedProcess(arguments, 0, (name + ' (unconfined)\n').encode() if added and not removed else b'', b'')
            if arguments[-2:] == ['diagnostics', 'document-core']:
                return subprocess.CompletedProcess(arguments, 1, b'', b'controlled diagnostic failure')
            return subprocess.CompletedProcess(arguments, 0, b'', b'')
        return binding, policy, commands, execute

    def test_failed_diagnostic_removes_only_its_exact_profile_and_retains_status(self):
        binding, policy, commands, execute = self.fixture()
        with patch.object(owner, 'selected', return_value=binding), patch.object(owner, 'authority', return_value={'owned': True}), \
                patch.object(owner, 'POLICY', policy), patch.object(owner.subprocess, 'run', side_effect=execute), \
                patch.object(owner, 'diagnostic', return_value=subprocess.CompletedProcess([], 1, b'', b'controlled diagnostic failure')):
            report = owner.run(Path('/tmp/package'))
        self.assertEqual(report['status'], 'failed')
        self.assertEqual(report['diagnostic_exit_code'], 1)
        self.assertTrue(report['profile_loaded'])
        self.assertTrue(report['profile_removed'])
        self.assertFalse(report['global_policy_changed'])
        self.assertIn(['sudo', '-n', 'apparmor_parser', '--add'], commands)
        self.assertIn(['sudo', '-n', 'apparmor_parser', '--remove'], commands)
        self.assertFalse(any('--replace' in command for command in commands))

    def test_preexisting_profile_refuses_before_add_or_remove(self):
        binding, policy, commands, execute = self.fixture()
        name, _ = owner.profile_source(binding['pw']['path'])
        with patch.object(owner, 'selected', return_value=binding), patch.object(owner, 'authority', return_value={'owned': True}), \
                patch.object(owner, 'POLICY', policy), patch.object(owner, 'profiles', return_value=[name + ' (unconfined)']), \
                patch.object(owner.subprocess, 'run', side_effect=execute):
            report = owner.run(Path('/tmp/package'))
        self.assertEqual(report['status'], 'failed')
        self.assertFalse(report['profile_loaded'])
        self.assertEqual(commands, [])

    def test_parser_timeout_after_load_removes_only_its_attempted_profile(self):
        binding, policy, commands, execute = self.fixture()
        def timeout_after_effect(arguments, **kwargs):
            if arguments[-1] == '--add':
                commands.append(arguments)
                raise subprocess.TimeoutExpired(arguments, 30)
            return execute(arguments, **kwargs)
        with patch.object(owner, 'selected', return_value=binding), patch.object(owner, 'authority', return_value={'owned': True}), \
                patch.object(owner, 'POLICY', policy), patch.object(owner.subprocess, 'run', side_effect=timeout_after_effect):
            report = owner.run(Path('/tmp/package'))
        self.assertEqual(report['status'], 'failed')
        self.assertTrue(report['profile_add_attempted'])
        self.assertTrue(report['profile_loaded'])
        self.assertTrue(report['profile_removed'])
        self.assertIn(['sudo', '-n', 'apparmor_parser', '--remove'], commands)

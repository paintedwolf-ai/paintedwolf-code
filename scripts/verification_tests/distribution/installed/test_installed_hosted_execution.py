"""Hosted desktop setup refuses shared machines and preserves existing filesystem owners."""
from pathlib import Path
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.hosted_desktop import fresh, run, validate_host


class HostedDesktopTests(unittest.TestCase):
    def environment(self):
        return {'GITHUB_ACTIONS': 'true', 'RUNNER_ENVIRONMENT': 'github-hosted',
                'GITHUB_RUN_ID': '123', 'GITHUB_SHA': 'a' * 40, 'RUNNER_ARCH': 'ARM64'}

    def test_preparation_requires_explicit_disposable_native_host(self):
        release = {'ID': 'ubuntu', 'VERSION_ID': '24.04'}
        validate_host(self.environment(), 0, 'aarch64', release)
        for change in [{'RUNNER_ENVIRONMENT': 'self-hosted'}, {'GITHUB_ACTIONS': 'false'},
                       {'GITHUB_RUN_ID': ''}, {'GITHUB_SHA': 'foreign'}, {'RUNNER_ARCH': 'X64'}]:
            with self.subTest(change=change), self.assertRaises(ValueError):
                validate_host(dict(self.environment(), **change), 0, 'aarch64', release)
        for uid, machine, distribution in [(1000, 'aarch64', release), (0, 'x86_64', release),
                                           (0, 'aarch64', {'ID': 'ubuntu', 'VERSION_ID': '26.04'})]:
            with self.subTest(uid=uid, machine=machine), self.assertRaises(ValueError):
                validate_host(self.environment(), uid, machine, distribution)

    def test_fresh_configuration_never_overwrites_a_file_or_follows_a_symlink(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            existing = root / 'existing'
            fresh(existing, 'owner', 0o600)
            with self.assertRaises(FileExistsError):
                fresh(existing, 'replacement')
            link = root / 'link'
            link.symlink_to(existing)
            with self.assertRaises(OSError):
                fresh(link, 'replacement')
            self.assertEqual(existing.read_text(), 'owner')
            self.assertEqual(stat.S_IMODE(existing.stat().st_mode), 0o600)

    def test_command_failure_never_echoes_password_input_or_stderr(self):
        result = SimpleNamespace(returncode=1, stdout=b'', stderr=b'sensitive-fixture')
        with patch('linux_release.hosted_desktop.subprocess.run', return_value=result) as command:
            with self.assertRaises(OSError) as error:
                run(['chpasswd'], input=b'private-input')
            self.assertNotIn('sensitive', str(error.exception))
            self.assertNotIn('private', str(error.exception))
            self.assertEqual(command.call_args.kwargs['input'], b'private-input')

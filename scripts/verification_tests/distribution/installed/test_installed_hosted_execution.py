"""Hosted desktop setup refuses shared machines and preserves existing filesystem owners."""
from pathlib import Path
import os
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.hosted_desktop import desktop_record, fresh, run, selected_controller, validate_host
from linux_release import hosted_desktop as hosted


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

    def test_immutable_controller_selection_keeps_workflow_identity_separate(self):
        environment = self.environment()
        self.assertEqual(selected_controller(environment, None), 'a' * 40)
        self.assertEqual(selected_controller(environment, 'b' * 40), 'b' * 40)
        self.assertEqual(environment['GITHUB_SHA'], 'a' * 40)
        for selected in ('main', 'refs/qualification/' + 'b' * 40, 'B' * 40, '', 123):
            with self.subTest(selected=selected), self.assertRaises(ValueError):
                selected_controller(environment, selected)

    def test_measured_session_cannot_replace_the_immutable_controller_identity(self):
        session = {'Desktop': 'GNOME', 'Name': 'gnome-xorg', 'Type': 'x11', 'Active': 'yes'}
        result = desktop_record(self.environment(), 'b' * 40, Path('/owned/controller'), session,
                                SimpleNamespace(pw_uid=1002))
        self.assertEqual((result['controller_commit'], result['workflow_commit']), ('b' * 40, 'a' * 40))
        self.assertEqual(result['session'], session)
        self.assertIs(result['application_qualified'], False)
        self.assertIs(result['physical_gpu_qualified'], False)
        with self.assertRaises(ValueError):
            desktop_record(self.environment(), 'gnome-xorg', Path('/owned/controller'), session,
                           SimpleNamespace(pw_uid=1002))

    def test_owned_host_prepares_the_secure_parent_without_relaxing_package_verification(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory).resolve()
            actual_fstat, actual_lstat = os.fstat, Path.lstat
            def root_owned(info):
                return SimpleNamespace(st_dev=info.st_dev, st_ino=info.st_ino, st_mode=info.st_mode,
                                       st_uid=0, st_gid=0)
            with patch.object(hosted, 'INSTALL_PARENT', path), patch.object(hosted, 'validate_host') as guard, \
                    patch.object(hosted.os, 'fstat', side_effect=lambda fd: root_owned(actual_fstat(fd))), \
                    patch.object(Path, 'lstat', lambda item: root_owned(actual_lstat(item))):
                path.chmod(0o777)
                result = hosted.prepare_install_parent()
                self.assertEqual((result['before_mode'], result['prepared_mode']), (0o777, 0o755))
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o755)
                self.assertEqual(hosted.prepare_install_parent()['before_mode'], 0o755)
                guard.assert_called()
                path.chmod(0o750)
                with self.assertRaises(ValueError):
                    hosted.prepare_install_parent()
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o750)
                path.chmod(0o777)
                (path / 'painted-wolf-code').mkdir()
                with self.assertRaises(ValueError):
                    hosted.prepare_install_parent()
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o777)

    def test_secure_parent_preparation_cannot_run_on_an_unselected_host(self):
        with patch.object(hosted, 'validate_host', side_effect=ValueError('unselected')), \
                patch.object(hosted.os, 'open') as opened:
            with self.assertRaises(ValueError):
                hosted.prepare_install_parent()
            opened.assert_not_called()

    def test_shared_parent_preparation_is_limited_to_the_second_fixed_hosted_ancestor(self):
        import os
        import stat
        from types import SimpleNamespace
        from tempfile import TemporaryDirectory
        with TemporaryDirectory() as directory:
            path = Path(directory).resolve()
            actual_fstat, actual_lstat = os.fstat, Path.lstat
            def root_owned(info):
                return SimpleNamespace(st_dev=info.st_dev, st_ino=info.st_ino, st_mode=info.st_mode,
                                       st_uid=0, st_gid=0)
            with patch.object(hosted, 'SHARED_PARENT', path), patch.object(hosted, 'validate_host'), \
                    patch.object(hosted.os, 'fstat', side_effect=lambda fd: root_owned(actual_fstat(fd))), \
                    patch.object(Path, 'lstat', lambda item: root_owned(actual_lstat(item))):
                path.chmod(0o777)
                result = hosted.prepare_install_parent(path)
                self.assertEqual(result['path'], str(path))
                self.assertEqual((result['before_mode'], result['prepared_mode']), (0o777, 0o755))
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o755)
                with patch.object(hosted.os, 'open') as opened:
                    with self.assertRaises(ValueError):
                        hosted.prepare_install_parent(path / 'undeclared')
                    opened.assert_not_called()

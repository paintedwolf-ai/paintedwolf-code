"""Hosted desktop setup refuses shared machines and preserves existing filesystem owners."""
from pathlib import Path
from contextlib import redirect_stdout, redirect_stderr
import io
import json
import os
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.hosted_desktop import desktop_record, fresh, run, selected_controller, validate_host
from linux_release import hosted_desktop as hosted
from linux_release.installed_environment import GraphicalSessionObservation


class HostedDesktopTests(unittest.TestCase):
    def setUp(self):
        release = patch.object(hosted, 'release', return_value={'ID': 'ubuntu', 'VERSION_ID': '24.04'})
        release.start()
        self.addCleanup(release.stop)

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
            self.assertNotIn('sensitive-fixture', str(error.exception))
            self.assertNotIn('private-input', str(error.exception))
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

    def test_declared_desktop_ancestor_uses_same_physical_owner_contract(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory).resolve()
            actual_fstat, actual_lstat = os.fstat, Path.lstat
            def root_owned(info):
                return SimpleNamespace(st_dev=info.st_dev, st_ino=info.st_ino, st_mode=info.st_mode, st_uid=0, st_gid=0)
            with patch.object(hosted, 'DESKTOP_PARENTS', (path,)), patch.object(hosted, 'validate_host'), \
                    patch.object(hosted.os, 'fstat', side_effect=lambda fd: root_owned(actual_fstat(fd))), \
                    patch.object(Path, 'lstat', lambda item: root_owned(actual_lstat(item))):
                path.chmod(0o777)
                observed = hosted.prepare_install_parent(path)
                self.assertEqual((observed['before_mode'], stat.S_IMODE(path.stat().st_mode)), (0o777, 0o755))
                with self.assertRaises(ValueError):
                    hosted.prepare_install_parent(path / 'unselected')

    def test_debian_shared_fragment_ancestors_are_explicitly_prepared(self):
        import runpy
        source = Path(__file__).resolve().parents[4] / 'scripts/linux-package-config.py'
        catalog = runpy.run_path(str(source))
        destinations = [Path(value[0]) for value in catalog['OPTIONAL_FRAGMENTS'].values()
                        if 'deb' in value[2] and value[0].startswith('/usr/share/')]
        destinations += [Path('/usr/share/lintian/overrides'), Path('/usr/share/applications')]
        declared = {hosted.SHARED_PARENT, *hosted.DESKTOP_PARENTS}
        for destination in destinations:
            for ancestor in (destination, *destination.parents):
                if ancestor == Path('/usr/share') or Path('/usr/share') in ancestor.parents:
                    self.assertIn(ancestor, declared, 'changed package footprint requires an explicit hosted ownership review')


class HostedGraphicalProbeTests(unittest.TestCase):
    def refused(self, code=None):
        return {'status': 'refused', 'observations': {'graphical_session': {
            'phase': 'properties-before', 'session_id': '7', 'property': 'User',
            **({'code': code} if code else {'remote_name': 'org.freedesktop.DBus.Error.AccessDenied'})}}}

    def test_probe_serializes_the_actual_observer_contract_and_injected_deadline(self):
        session = {'Type': 'x11', 'Active': 'yes'}
        with patch.object(hosted, 'graphical_session', return_value=GraphicalSessionObservation(session, {})) as observe, redirect_stdout(io.StringIO()) as output:
            hosted.write_graphical_probe(deadline=42)
        self.assertEqual(json.loads(output.getvalue()), {'status': 'observed', 'session': session, 'observations': {}})
        observe.assert_called_once_with(deadline=42)
        facts = self.refused()['observations']
        failure = hosted.CaseFailed('typed fixture refusal')
        failure.observations = facts
        with patch.object(hosted, 'graphical_session', side_effect=failure), redirect_stdout(io.StringIO()) as output:
            hosted.write_graphical_probe(deadline=42)
        self.assertEqual(json.loads(output.getvalue()), self.refused())

    def test_hosted_readiness_retries_only_structured_no_match_then_retains_it(self):
        waiting = self.refused('GRAPHICAL_SESSION_NOT_READY')
        session = {'Type': 'x11', 'Active': 'yes'}
        replies = [json.dumps(waiting).encode(), json.dumps({'status': 'observed', 'session': session, 'observations': {}}).encode()]
        with patch.object(hosted, 'run', side_effect=replies) as run, patch.object(hosted.time, 'monotonic', return_value=0), patch.object(hosted.time, 'sleep') as sleep:
            actual, readiness = hosted.await_hosted_session(['owned-probe'], Path('/owned'), deadline=1)
        self.assertEqual(actual, session)
        self.assertEqual(readiness, {'not_ready_observations': 1, 'last_not_ready': waiting['observations'], 'selected_observation': {}})
        self.assertEqual(run.call_count, 2)
        self.assertEqual(sleep.call_count, 1)

    def test_actual_typed_refusal_is_not_silently_retried_or_discarded(self):
        refused = self.refused()
        with patch.object(hosted, 'run', return_value=json.dumps(refused).encode()) as run, patch.object(hosted.time, 'monotonic', return_value=0), patch.object(hosted.time, 'sleep') as sleep, self.assertRaises(hosted.CaseFailed) as failure:
            hosted.await_hosted_session(['owned-probe'], Path('/owned'), deadline=1)
        self.assertEqual(failure.exception.observations, refused['observations'])
        self.assertEqual(run.call_count, 1)
        sleep.assert_not_called()

    def test_probe_transport_and_malformed_frames_refuse_with_the_same_contract(self):
        for raw in (b'not-json', b'{}', b'[]', b'{"status":"refused","observations":null}', b'{"status":"observed","session":{"Type":"wayland","Active":"yes"}}'):
            with self.subTest(raw=raw), patch.object(hosted, 'run', return_value=raw), patch.object(hosted.time, 'monotonic', return_value=0), self.assertRaises(hosted.CaseFailed):
                hosted.await_hosted_session(['owned-probe'], Path('/owned'), deadline=1)
        with patch.object(hosted, 'run', side_effect=OSError('fixture transport')), patch.object(hosted.time, 'monotonic', return_value=0), self.assertRaises(hosted.CaseFailed) as failure:
            hosted.await_hosted_session(['owned-probe'], Path('/owned'), deadline=1)
        self.assertEqual(failure.exception.observations['graphical_session']['error_type'], 'OSError')

    def test_readiness_deadline_and_cli_preserve_structured_failure_facts(self):
        waiting = self.refused('GRAPHICAL_SESSION_NOT_READY')
        with patch.object(hosted, 'run', return_value=json.dumps(waiting).encode()), patch.object(hosted.time, 'monotonic', side_effect=[0, 2]), patch.object(hosted.time, 'sleep'), self.assertRaises(hosted.CaseFailed) as failure:
            hosted.await_hosted_session(['owned-probe'], Path('/owned'), deadline=1)
        self.assertEqual(failure.exception.observations['graphical_session_readiness']['last_not_ready'], waiting['observations'])
        with patch.object(hosted, 'prepare', side_effect=failure.exception), redirect_stderr(io.StringIO()) as error:
            code = hosted.main(['--checkout', '/owned', '--output', '/owned/result'])
        self.assertEqual(code, 2)
        self.assertEqual(json.loads(error.getvalue())['observations'], failure.exception.observations)

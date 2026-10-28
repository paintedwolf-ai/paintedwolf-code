"""The Nix workflow exercise runs ordinary and approved phases through the installed command."""
import errno
import json
from pathlib import Path
import shlex
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import nix_workflows as nix
from linux_release.installed_cases.records import CaseUnavailable

CLIENT = '/nix/store/' + 'a' * 32 + '-nix/bin/nix'
STORE = '/nix/store/' + 'b' * 32 + '-paintedwolf-code'
IDENTITY = {'sha256': 'c' * 64, 'bytes': 42, 'mode': 0o555, 'device': 1, 'inode': 2}
SESSION = '11111111-1111-4111-8111-111111111111'
ORDINARY = {'nonce': 'n', 'uid': 1000, 'pid_namespace': 22, 'exit_code': 1, 'socket_errno': errno.EACCES, 'stdout': ''}
APPROVED = {'nonce': 'n', 'uid': 1000, 'pid_namespace': 11, 'exit_code': 0, 'socket_errno': 0,
            'stdout': json.dumps({'version': '2.24.0'})}


def binding() -> dict:
    return {'artifact': {'identity': {'kind': 'nix-closure', 'store_path': STORE}, 'installation': 'nix'},
            'payload': {'nix_client': dict(IDENTITY, path=CLIENT)}, 'source_commit': 'd' * 40}


class ClientTests(unittest.TestCase):
    def physical(self, identity=IDENTITY, socket=None):
        socket = SimpleNamespace(st_mode=stat.S_IFSOCK | 0o666, st_uid=0) if socket is None else socket
        return [patch.object(Path, 'resolve', lambda self, strict=False: self),
                patch.object(Path, 'stat', lambda self: socket),
                patch.object(nix, 'file_identity', return_value=dict(identity, header=b'ELF'))]

    def select(self, patches, selected):
        for item in patches:
            item.start()
        try:
            return nix.client(selected)
        finally:
            for item in patches:
                item.stop()

    def test_the_retained_physical_client_behind_a_root_daemon_socket_is_selected(self):
        self.assertEqual(self.select(self.physical(), binding()), Path(CLIENT))

    def test_missing_closure_or_client_is_unavailable(self):
        selected = binding()
        selected['artifact']['identity']['kind'] = 'deb'
        with self.assertRaises(CaseUnavailable):
            nix.client(selected)
        selected = binding()
        selected['payload'] = {'nix_client': {'sha256': 'c' * 64}}
        with self.assertRaises(CaseUnavailable):
            nix.client(selected)

    def test_a_changed_writable_or_unplaced_client_is_refused(self):
        with self.assertRaises(ValueError):
            self.select(self.physical(identity=dict(IDENTITY, inode=3)), binding())
        with self.assertRaises(ValueError):
            self.select(self.physical(identity=dict(IDENTITY, mode=0o755)), binding())
        selected = binding()
        selected['payload']['nix_client']['path'] = '/usr/bin/nix'
        with self.assertRaises(ValueError):
            self.select(self.physical(), selected)

    def test_a_missing_or_user_owned_daemon_socket_is_unavailable(self):
        with self.assertRaises(CaseUnavailable):
            self.select(self.physical(socket=SimpleNamespace(st_mode=stat.S_IFSOCK | 0o666, st_uid=1000)), binding())
        with self.assertRaises(CaseUnavailable):
            self.select(self.physical(socket=SimpleNamespace(st_mode=stat.S_IFREG | 0o666, st_uid=0)), binding())


class WitnessTests(unittest.TestCase):
    def test_the_native_observation_is_bound_to_its_nonce_and_shape(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(nix.os, 'getuid', return_value=1000):
            turn = SimpleNamespace(workspace=Path(temporary))
            path = turn.workspace / 'nix-observation.json'
            path.write_text(json.dumps(ORDINARY))
            self.assertEqual(nix.witness(turn, 'n'), ORDINARY)
            with self.assertRaises(ValueError):
                nix.witness(turn, 'other')
            for changed in [dict(ORDINARY, uid=0), dict(ORDINARY, exit_code='1'), dict(ORDINARY, stdout=None)]:
                path.write_text(json.dumps(changed))
                with self.subTest(changed=changed), self.assertRaises(ValueError):
                    nix.witness(turn, 'n')
            path.write_text('x' * 131073)
            with self.assertRaises(CaseUnavailable):
                nix.witness(turn, 'n')
            path.unlink()
            clock = iter([0.0, 1000.0])
            with patch.object(nix.time, 'monotonic', lambda: next(clock, 2000.0)), self.assertRaises(CaseUnavailable):
                nix.witness(turn, 'n')


class Exercise:
    """Patch the installed collaborators around the two phases of one exercise."""

    def __init__(self, natives=(ORDINARY, APPROVED), completed=True, store=True):
        self.natives, self.completed, self.store = list(natives), completed, store
        self.app = MagicMock()
        self.app.observations = {'capture_complete': True}
        self.app.streams.return_value = {'stdout': b'engine'}
        self.turns = []
        native = nix.Path

        class NamespacePath:
            def stat(self):
                return SimpleNamespace(st_ino=11)
        self.path = lambda value='.': NamespacePath() if value == '/proc/self/ns/pid' else native(value)

    def tool_turn(self, app, provider, project):
        turn = MagicMock()
        turn.session_id = SESSION
        turn.observations = {'turn': len(self.turns)}
        outcome = 'completed' if self.completed else 'rejected'
        turn.wait.return_value = [{'tool': 'command', 'outcome': outcome, 'invocation': {'invoked': True}}]
        self.turns.append(turn)
        manager = MagicMock()
        manager.__enter__.return_value = turn
        return manager

    def __enter__(self):
        app_manager = MagicMock()
        app_manager.__enter__.return_value = self.app
        provider = MagicMock()
        provider.observations = {'provider': 'facts'}
        provider_manager = MagicMock()
        provider_manager.__enter__.return_value = provider
        self.patches = [patch.object(nix, 'client', return_value=Path(CLIENT)),
                        patch.object(nix, 'source_target', return_value='git+file:///checkout?rev=' + 'd' * 40),
                        patch.object(nix, 'Path', side_effect=self.path),
                        patch.object(nix, 'InstalledApp', MagicMock(return_value=app_manager)),
                        patch.object(nix, 'ProviderFixture', MagicMock(return_value=provider_manager)),
                        patch.object(nix, 'ToolTurn', side_effect=self.tool_turn),
                        patch.object(nix, 'approve_host_execution'),
                        patch.object(nix, 'witness', side_effect=lambda turn, nonce: self.natives.pop(0)),
                        patch.object(nix, 'wait_settled'),
                        patch.object(nix, 'store_facts', return_value=self.store)]
        self.mocks = [item.start() for item in self.patches]
        return self

    def __exit__(self, *_):
        for item in self.patches:
            item.stop()

    def arguments(self, index: int) -> dict:
        return self.mocks[4].call_args_list[index].args[0][0]['arguments']


class ExerciseTests(unittest.TestCase):
    def test_daemon_authority_runs_an_ordinary_and_an_approved_phase(self):
        with Exercise() as exercise:
            status, facts, streams = nix.exercise(binding(), 'daemon')
        self.assertEqual(status, 'passed')
        self.assertEqual(streams, {'stdout': b'engine'})
        self.assertEqual(facts['client'], CLIENT)
        self.assertEqual(facts['source_target'], '')
        self.assertEqual(facts['host_pid_namespace'], 11)
        self.assertEqual([row['approved'] for row in facts['phases']], [False, True])
        self.assertEqual([row['native'] for row in facts['phases']], [ORDINARY, APPROVED])
        self.assertEqual(facts['app'], {'capture_complete': True})
        self.assertNotIn('capability_request', exercise.arguments(0))
        self.assertEqual(exercise.arguments(1)['capability_request'], {'host_execution': True})
        command = shlex.split(exercise.arguments(1)['command'])
        self.assertEqual(command[2:4], [CLIENT, nix.SOCKET])
        self.assertEqual(command[5:], ['daemon', '', STORE])
        self.assertTrue(command[1].endswith('nix-workflow.py'))
        exercise.mocks[1].assert_not_called()
        exercise.mocks[6].assert_called_once_with(exercise.turns[1], exercise.arguments(1))
        self.assertEqual(exercise.mocks[0].call_count, 2)
        self.assertEqual(exercise.mocks[8].call_count, 2)

    def test_the_dev_shell_phase_needs_the_shell_marker_and_the_store_identity(self):
        shell = dict(APPROVED, stdout='paintedwolf-nix-dev-shell')
        with Exercise(natives=(ORDINARY, shell)) as exercise:
            status, facts, _ = nix.exercise(binding(), 'develop')
        self.assertEqual(status, 'passed')
        self.assertEqual(facts['source_target'], 'git+file:///checkout?rev=' + 'd' * 40)
        self.assertEqual(shlex.split(exercise.arguments(0)['command'])[5:7], ['develop', facts['source_target']])
        exercise.mocks[9].assert_called_once()
        with Exercise(natives=(ORDINARY, shell), store=False):
            status, _, _ = nix.exercise(binding(), 'develop')
        self.assertEqual(status, 'failed')
        with Exercise(natives=(ORDINARY, APPROVED)):
            status, _, _ = nix.exercise(binding(), 'develop')
        self.assertEqual(status, 'failed')

    def test_an_ordinary_phase_that_reaches_the_daemon_fails(self):
        with Exercise(natives=(dict(ORDINARY, socket_errno=0, exit_code=0), APPROVED)):
            status, _, _ = nix.exercise(binding(), 'daemon')
        self.assertEqual(status, 'failed')

    def test_an_incomplete_installed_command_is_unavailable(self):
        with Exercise(completed=False), self.assertRaises(CaseUnavailable):
            nix.exercise(binding(), 'daemon')


class ExecuteTests(unittest.TestCase):
    def test_each_development_case_is_dispatched_to_its_owner_after_the_guest_check(self):
        root = Path('/unused')
        with patch.object(nix, 'require_owned_guest') as guest, \
                patch.object(nix, 'retain', side_effect=lambda *args: args) as retain, \
                patch('linux_release.installed_cases.nix_remote.scenario', return_value=('passed', {'remote': 1}, {})) as remote, \
                patch('linux_release.installed_cases.nix_cancellation.scenario', return_value=('failed', {'cancel': 1}, {})) as cancel, \
                patch.object(nix, 'exercise', return_value=('passed', {'mode': 1}, {'stdout': b''})) as exercise:
            self.assertEqual(nix.execute(binding(), 'development', 'remote-builder-authority', root)[4:],
                             ('passed', {'remote': 1}, {}))
            self.assertEqual(nix.execute(binding(), 'development', 'cancellation-and-no-silent-escalation', root)[4:],
                             ('failed', {'cancel': 1}, {}))
            self.assertEqual(nix.execute(binding(), 'development', 'daemon-authority-and-attribution', root)[4:],
                             ('passed', {'mode': 1}, {'stdout': b''}))
            nix.execute(binding(), 'development', 'nix-store-and-dev-shell', root)
            self.assertEqual([call.args[1] for call in exercise.call_args_list], ['daemon', 'develop'])
            remote.assert_called_once_with(binding())
            cancel.assert_called_once_with(binding())
            self.assertEqual(guest.call_count, 4)
            self.assertEqual(retain.call_count, 4)
            self.assertEqual(nix.execute(binding(), 'development', 'generation-cache', root)[4], 'unverified')
            self.assertEqual(guest.call_count, 4)


if __name__ == '__main__':
    unittest.main()

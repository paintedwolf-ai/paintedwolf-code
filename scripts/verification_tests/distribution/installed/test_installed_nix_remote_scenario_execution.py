"""The installed SSH Nix case admits only its provisioned independent builder and runs two phases."""
import base64
import errno
import hashlib
from pathlib import Path
import shlex
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import nix_remote as remote
from linux_release.installed_cases import nix_workflows
from linux_release.installed_cases.records import CaseUnavailable

CLIENT = '/nix/store/' + 'a' * 32 + '-nix/bin/nix'
DERIVATION = '/nix/store/' + 'b' * 32 + '-paintedwolf-nix-remote-' + 'c' * 32 + '.drv'
OUTPUT = '/nix/store/' + 'd' * 32 + '-paintedwolf-nix-remote-' + 'c' * 32
URI = 'ssh-ng://pw-nix-builder@10.0.2.2:39337'
PUBLIC = b'ssh-ed25519 AAAAclient fixture@client\n'
HOST_KEY = b'ssh-ed25519 AAAAhost fixture@controller'
KNOWN = Path('/etc/paintedwolf-nix-builder-known-hosts')
KNOWN_HOSTS = b'[10.0.2.2]:39337 ssh-ed25519 AAAAhost\n'
SESSION = '11111111-1111-4111-8111-111111111111'
ORDINARY = {'nonce': 'n', 'uid': 1000, 'pid_namespace': 22, 'exit_code': 1, 'socket_errno': errno.EACCES, 'stdout': ''}
APPROVED = {'nonce': 'n', 'uid': 1000, 'pid_namespace': 11, 'exit_code': 0, 'socket_errno': 0, 'stdout': '[]'}


def fixture(home: Path) -> tuple[dict, dict]:
    key = home / '.local/share/paintedwolf-nix-builder/id_ed25519'
    server = {'format_version': 1, 'purpose': 'independent-nix-ssh-builder-fixture', 'account': 'pw-nix-builder',
              'forced_command': '/nix/store/' + 'a' * 32 + '-nix/bin/nix-daemon --stdio',
              'client': {'path': CLIENT, 'sha256': 'e' * 64}, 'host_public_key': base64.b64encode(HOST_KEY).decode(),
              'client_public_key_sha256': hashlib.sha256(PUBLIC).hexdigest(), 'machine_id_sha256': 'f' * 64,
              'system': 'x86_64-linux', 'sandbox': True}
    builder = {'uri': URI, 'server': server, 'ssh_key': str(key), 'known_hosts': str(KNOWN),
               'known_hosts_sha256': hashlib.sha256(KNOWN_HOSTS).hexdigest(), 'derivation': DERIVATION,
               'derivation_sha256': '1' * 64, 'output': OUTPUT, 'token': 'c' * 32, 'arguments': ['sh', '-c', 'x']}
    record = {'client': server['client'], 'system': 'x86_64-linux', 'remote_builder': builder}
    return record, builder


class Provisioned:
    """A home with the dedicated identity, a root-controlled known-hosts file and a mocked Nix client."""

    def __init__(self, *, keygen_output=PUBLIC, known_hosts=KNOWN_HOSTS, machine_id=b'controller-differs\n',
                 realized=False, keygen_present=True, key_mode=0o600):
        self.keygen_output, self.known_hosts, self.machine_id = keygen_output, known_hosts, machine_id
        self.realized, self.keygen_present, self.key_mode = realized, keygen_present, key_mode

    def __enter__(self):
        self.temporary = tempfile.TemporaryDirectory()
        home = Path(self.temporary.name)
        self.record, self.builder = fixture(home)
        key = home / '.local/share/paintedwolf-nix-builder/id_ed25519'
        key.parent.mkdir(parents=True)
        key.write_bytes(b'private')
        key.chmod(self.key_mode)
        Path(str(key) + '.pub').write_bytes(PUBLIC)
        native_read, native_lstat, native_exists = Path.read_bytes, Path.lstat, Path.exists

        def read_bytes(path):
            if str(path) == '/etc/machine-id':
                return self.machine_id
            if path == KNOWN:
                return self.known_hosts
            return native_read(path)

        def lstat(path):
            if path == KNOWN:
                return SimpleNamespace(st_mode=stat.S_IFREG | 0o644, st_uid=0, st_size=len(self.known_hosts))
            return native_lstat(path)

        def exists(path):
            if path.name == 'ssh-keygen':
                return self.keygen_present
            return native_exists(path)
        keygen = SimpleNamespace(returncode=0, stdout=self.keygen_output)
        self.patches = [patch.object(Path, 'home', return_value=home), patch.object(Path, 'read_bytes', read_bytes),
                        patch.object(Path, 'lstat', lstat), patch.object(Path, 'exists', exists),
                        patch.object(remote.subprocess, 'run', return_value=keygen),
                        patch.object(remote, 'load', return_value=self.record),
                        patch.object(remote, 'store_file'),
                        patch.object(remote, 'nix_command', return_value='{"version": 4}'),
                        patch.object(remote, 'derivation'),
                        patch.object(remote, 'realized', return_value=self.realized)]
        self.mocks = [item.start() for item in self.patches]
        return self

    def __exit__(self, *_):
        for item in self.patches:
            item.stop()
        self.temporary.cleanup()


class SelectTests(unittest.TestCase):
    def test_the_provisioned_independent_builder_is_selected(self):
        with Provisioned() as provisioned:
            record, builder = remote.select({'artifact': {'architecture': 'amd64'}})
        self.assertIs(record, provisioned.record)
        self.assertIs(builder, provisioned.builder)
        provisioned.mocks[6].assert_called_once_with({'path': DERIVATION, 'sha256': '1' * 64}, {'path', 'sha256'})
        provisioned.mocks[7].assert_called_once_with(Path(CLIENT), 'derivation', 'show', DERIVATION)
        provisioned.mocks[8].assert_called_once_with({'version': 4}, provisioned.record, 'remote_builder')
        provisioned.mocks[9].assert_called_once_with(Path(CLIENT), OUTPUT)
        self.assertEqual(provisioned.mocks[4].call_args.args[0][1:], ['-y', '-f', provisioned.builder['ssh_key']])

    def test_an_unprovisioned_realized_or_unobservable_fixture_is_unavailable(self):
        with Provisioned() as provisioned:
            provisioned.record['remote_builder'] = None
            with self.assertRaises(CaseUnavailable):
                remote.select({})
        with Provisioned(realized=True), self.assertRaises(CaseUnavailable):
            remote.select({})
        with Provisioned(keygen_present=False), self.assertRaises(CaseUnavailable):
            remote.select({})

    def test_a_server_without_independent_sandboxed_identity_is_refused(self):
        for change in [{'sandbox': False}, {'account': 'root'}, {'system': 'aarch64-linux'}, {'format_version': 2},
                       {'machine_id_sha256': 'f' * 63}, {'forced_command': '/bin/sh'}, {'extra': True},
                       {'client': {'path': '/usr/bin/nix', 'sha256': 'e' * 64}}]:
            with self.subTest(change=change), Provisioned() as provisioned:
                provisioned.builder['server'].update(change)
                with self.assertRaises(ValueError):
                    remote.select({})
        with Provisioned(machine_id=b'same\n') as provisioned:
            provisioned.builder['server']['machine_id_sha256'] = hashlib.sha256(b'same\n').hexdigest()
            with self.assertRaises(ValueError):
                remote.select({})
        with Provisioned() as provisioned:
            provisioned.builder['uri'] = 'ssh://pw-nix-builder@10.0.2.2:22'
            with self.assertRaises(ValueError):
                remote.select({})

    def test_the_identity_must_be_the_dedicated_private_key_the_controller_authorized(self):
        with Provisioned() as provisioned:
            provisioned.builder['ssh_key'] = '/tmp/other-identity'
            with self.assertRaises(ValueError):
                remote.select({})
        with Provisioned(key_mode=0o644), self.assertRaises(ValueError):
            remote.select({})
        with Provisioned() as provisioned:
            provisioned.builder['server']['client_public_key_sha256'] = '0' * 64
            with self.assertRaises(ValueError):
                remote.select({})
        with Provisioned(keygen_output=b'ssh-ed25519 AAAAother\n'), self.assertRaises(ValueError):
            remote.select({})
        with Provisioned(keygen_output=b'') as provisioned:
            provisioned.mocks[4].return_value = SimpleNamespace(returncode=255, stdout=b'')
            with self.assertRaises(ValueError):
                remote.select({})

    def test_the_host_identity_must_be_the_root_controlled_known_hosts_entry(self):
        with Provisioned(known_hosts=b'[10.0.2.2]:39337 ssh-ed25519 AAAAother\n'), self.assertRaises(ValueError):
            remote.select({})
        with Provisioned() as provisioned:
            provisioned.builder['known_hosts'] = '/etc/ssh/ssh_known_hosts'
            with self.assertRaises(ValueError):
                remote.select({})
        with Provisioned() as provisioned:
            provisioned.builder['known_hosts_sha256'] = '0' * 64
            with self.assertRaises(ValueError):
                remote.select({})
        with Provisioned() as provisioned:
            provisioned.builder['server']['host_public_key'] = base64.b64encode(b'ssh-rsa AAAAhost comment').decode()
            with self.assertRaises(ValueError):
                remote.select({})


class Delegation:
    """Patch the installed collaborators around the ordinary and approved phases of one scenario."""

    def __init__(self, natives=(ORDINARY, APPROVED), completed=True, passed=True, stable=True):
        self.natives, self.completed, self.passed, self.stable = list(natives), completed, passed, stable
        self.turns = []

    def tool_turn(self, app, provider, project):
        turn = MagicMock()
        turn.session_id = SESSION
        turn.observations = {'turn': len(self.turns), 'project': str(project)}
        outcome = 'completed' if self.completed else 'rejected'
        turn.wait.return_value = [{'tool': 'command', 'outcome': outcome, 'invocation': {'invoked': True}}]
        self.turns.append(turn)
        manager = MagicMock()
        manager.__enter__.return_value = turn
        return manager

    def __enter__(self):
        self.record, self.builder = fixture(Path('/home/fixture'))
        self.app = MagicMock()
        self.app.observations = {'capture_complete': True}
        self.app.streams.return_value = {'stdout': b'engine'}
        app_manager = MagicMock()
        app_manager.__enter__.return_value = self.app
        provider = MagicMock()
        provider.observations = {'provider': 'facts'}
        provider_manager = MagicMock()
        provider_manager.__enter__.return_value = provider
        native = remote.Path

        class NamespacePath:
            def stat(self):
                return SimpleNamespace(st_ino=11)
        later = self.record if self.stable else dict(self.record, system='aarch64-linux')
        self.patches = [patch.object(nix_workflows, 'client', return_value=Path(CLIENT)),
                        patch.object(nix_workflows, 'witness', side_effect=lambda turn, nonce: self.natives.pop(0)),
                        patch.object(remote, 'select', return_value=(self.record, self.builder)),
                        patch.object(remote, 'Path', side_effect=lambda value='.': NamespacePath() if value == '/proc/self/ns/pid' else native(value)),
                        patch.object(remote, 'InstalledApp', MagicMock(return_value=app_manager)),
                        patch.object(remote, 'ProviderFixture', MagicMock(return_value=provider_manager)),
                        patch.object(remote, 'ToolTurn', side_effect=self.tool_turn),
                        patch.object(remote, 'approve_host_execution'),
                        patch.object(remote, 'wait_settled'),
                        patch.object(remote, 'successful', return_value=self.passed),
                        patch.object(remote, 'load', return_value=later)]
        self.mocks = [item.start() for item in self.patches]
        return self

    def __exit__(self, *_):
        for item in self.patches:
            item.stop()

    def arguments(self, index: int) -> dict:
        return self.mocks[5].call_args_list[index].args[0][0]['arguments']


class ScenarioTests(unittest.TestCase):
    def test_the_scenario_runs_an_ordinary_then_an_approved_delegation(self):
        with Delegation() as delegation:
            status, facts, streams = remote.scenario({'artifact': {'architecture': 'amd64'}})
        self.assertEqual(status, 'passed')
        self.assertEqual(streams, {'stdout': b'engine'})
        self.assertEqual((facts['endpoint'], facts['server'], facts['derivation']),
                         (URI, delegation.builder['server'], DERIVATION))
        self.assertEqual([row['native'] for row in facts['phases']], [ORDINARY, APPROVED])
        self.assertEqual([row['turn']['turn'] for row in facts['phases']], [0, 1])
        self.assertEqual([row['provider'] for row in facts['phases']], [{'provider': 'facts'}] * 2)
        self.assertEqual(facts['app'], {'capture_complete': True})
        self.assertNotIn('capability_request', delegation.arguments(0))
        self.assertEqual(delegation.arguments(1)['capability_request'], {'host_execution': True})
        for index, mode in enumerate(['ordinary', 'approved']):
            command = shlex.split(delegation.arguments(index)['command'])
            self.assertTrue(command[1].endswith('nix-remote.py'))
            self.assertEqual(command[3], mode)
            self.assertEqual(delegation.arguments(index)['timeout_ms'], 240000)
        delegation.mocks[7].assert_called_once_with(delegation.turns[1], delegation.arguments(1))
        delegation.mocks[9].assert_called_once_with(ORDINARY, APPROVED, delegation.builder, 11)
        self.assertEqual(delegation.mocks[0].call_count, 2)
        self.assertEqual(delegation.mocks[8].call_count, 2)
        self.assertEqual(delegation.mocks[2].call_count, 1)

    def test_a_delegation_that_misses_the_independent_outcome_fails(self):
        with Delegation(passed=False):
            status, facts, _ = remote.scenario({})
        self.assertEqual(status, 'failed')
        self.assertEqual(len(facts['phases']), 2)

    def test_an_incomplete_installed_command_is_unavailable(self):
        with Delegation(completed=False) as delegation, self.assertRaises(CaseUnavailable):
            remote.scenario({})
        self.assertEqual(len(delegation.turns), 1)

    def test_a_controller_that_changes_during_delegation_is_refused(self):
        with Delegation(stable=False), self.assertRaises(ValueError):
            remote.scenario({})


if __name__ == '__main__':
    unittest.main()

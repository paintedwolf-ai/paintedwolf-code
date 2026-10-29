"""Native security observers return typed facts and the vault case binds them to the selected service."""
import json
import os
from pathlib import Path
import stat
import struct
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import security
from linux_release.installed_cases.records import CaseFailed

IMAGE = '/opt/painted-wolf-code/libexec/pw-vault-identity'
REFUSED = {'status': 'refused', 'code': 'executable_mismatch', 'identity_returned': False, 'launcher_pid': 0}


class BrokenFrame:
    def __init__(self, body: bytes):
        self.raw = struct.pack('!I', len(body)) + body

    def sendall(self, _):
        raise BrokenPipeError

    def recv(self, size):
        raw, self.raw = self.raw[:size], self.raw[size:]
        return raw


def proc_tree(root: Path, pid: int, fields: int = 24, uid: str = '999 999 999 999', caps: str = '0000000000080000'):
    directory = root / str(pid)
    directory.mkdir()
    after = ['S', '1', str(pid), str(pid), '0', '-1'] + ['0'] * (fields - 6)
    if fields >= 20:
        after[19] = '777'
    (directory / 'stat').write_text(f'{pid} (pw vault) ' + ' '.join(after) + '\n')
    (directory / 'status').write_text(f'Name:\tpw-vault-identity\nUid:\t{uid}\nGid:\t999 999 999 999\nCapEff:\t{caps}\n')
    return directory


class VaultCase:
    """Patch every native collaborator of the wrong-launcher case."""

    def __init__(self, uids=(999, 999, 999, 999), image=IMAGE, socket_mode=stat.S_IFSOCK, caps=1 << 19,
                 pids=(4242, 4242), engine_passed=True):
        self.socket = MagicMock()
        self.socket.lstat.return_value = SimpleNamespace(st_mode=socket_mode, st_uid=0)
        self.socket.parent.stat.return_value = SimpleNamespace(st_mode=stat.S_IFDIR | 0o755, st_uid=0)
        self.identity = ('777', list(uids), caps)
        self.app = MagicMock()
        self.app.observations = {'engine_identity': {'pid': 10, 'start_time_ticks': 30},
                                 'launch_identity': {'pid': 11, 'start_time_ticks': 31}}
        self.app.streams.return_value = {'stdout': 'shell'}
        self.image, self.pids, self.engine_passed = image, pids, engine_passed

    def __enter__(self):
        manager = MagicMock()
        manager.__enter__.return_value = self.app
        self.patches = [patch.object(security, 'SOCKET', self.socket),
                        patch.object(security.socket, 'socket', MagicMock()),
                        patch.object(security, 'refusal', return_value=dict(REFUSED)),
                        patch.object(security, 'service_pid', side_effect=list(self.pids)),
                        patch.object(security, 'process_identity', return_value=self.identity),
                        patch.object(security, 'observe', return_value=(self.image + '\n').encode()),
                        patch.object(security, 'unchanged', return_value=Path(IMAGE)),
                        patch.object(security, 'memory_read_denied', return_value=True),
                        patch.object(security, 'InstalledApp', MagicMock(return_value=manager)),
                        patch.object(security, 'protected_process',
                                     return_value=(self.engine_passed, {'memory_read_denied': True}))]
        self.mocks = [item.start() for item in self.patches]
        return self

    def __exit__(self, *_):
        for item in self.patches:
            item.stop()


class SecurityObserverTests(unittest.TestCase):
    def test_native_observer_runs_in_the_c_locale_and_bounds_its_evidence(self):
        result = SimpleNamespace(returncode=0, stdout=b'{"type": "u"}', stderr=b'')
        with patch.object(security.subprocess, 'run', return_value=result) as run:
            self.assertEqual(security.observe(['busctl', 'status']), b'{"type": "u"}')
        self.assertEqual(run.call_args.args[0], ['busctl', 'status'])
        self.assertEqual(run.call_args.kwargs['env']['LC_ALL'], 'C')
        self.assertIs(run.call_args.kwargs['stdin'], subprocess.DEVNULL)
        for failed in [SimpleNamespace(returncode=1, stdout=b''), SimpleNamespace(returncode=0, stdout=b'x' * ((1 << 20) + 1))]:
            with patch.object(security.subprocess, 'run', return_value=failed), self.assertRaises(OSError):
                security.observe(['busctl', 'status'])

    def test_bus_values_must_carry_the_expected_type_and_an_array_payload(self):
        with patch.object(security, 'observe', return_value=json.dumps({'type': 'o', 'data': ['/unit']}).encode()) as observe:
            self.assertEqual(security.bus_value(['call', 'x'], 'o'), ['/unit'])
        self.assertEqual(observe.call_args.args[0], ['busctl', '--system', '--json=short', 'call', 'x'])
        for payload in [{'type': 'u', 'data': ['/unit']}, {'type': 'o', 'data': '/unit'}, ['/unit']]:
            with patch.object(security, 'observe', return_value=json.dumps(payload).encode()), self.assertRaises(ValueError):
                security.bus_value(['call', 'x'], 'o')

    def test_service_pid_requires_a_systemd_unit_path_and_a_running_main_process(self):
        pid = json.dumps({'type': 'u', 'data': 4242}).encode()
        with patch.object(security, 'bus_value', return_value=['/org/freedesktop/systemd1/unit/vault']), \
                patch.object(security, 'observe', return_value=pid):
            self.assertEqual(security.service_pid(), 4242)
        with patch.object(security, 'bus_value', return_value=['/org/freedesktop/other/vault']), self.assertRaises(ValueError):
            security.service_pid()
        with patch.object(security, 'bus_value', return_value=['/org/freedesktop/systemd1/unit/vault']), \
                patch.object(security, 'observe', return_value=json.dumps({'type': 'u', 'data': 0}).encode()), \
                self.assertRaises(OSError):
            security.service_pid()

    def test_process_identity_reads_start_time_uids_and_effective_capabilities(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            proc_tree(root, 4242)
            proc_tree(root, 4243, fields=12)
            fake = lambda value: root if value == '/proc' else Path(value)
            with patch.object(security, 'Path', side_effect=fake):
                self.assertEqual(security.process_identity(4242), ('777', [999, 999, 999, 999], 1 << 19))
                with self.assertRaises(ValueError):
                    security.process_identity(4243)

    def test_refusal_tolerates_a_closed_write_side_and_refuses_non_object_responses(self):
        self.assertEqual(security.refusal(BrokenFrame(b'{"status": "refused", "code": "executable_mismatch"}')),
                         {'status': 'refused', 'code': 'executable_mismatch', 'identity_returned': False, 'launcher_pid': 0})
        with self.assertRaises(ValueError):
            security.refusal(BrokenFrame(b'["refused"]'))

    def test_inhibitor_requires_the_captured_process_identity(self):
        context = {'Remote': False, 'Active': True, 'Type': 'wayland'}
        result = {'exit_code': 0, 'timed_out': False, 'output_limit': False, 'stdout': '', 'stderr': ''}
        with patch.object(security, 'execution_session', return_value=context), \
                patch.object(security, 'inhibitors', return_value=[]), \
                patch.object(security, 'run', return_value=result), self.assertRaises(ValueError):
            security.inhibitor_lifetime({})


class VaultLauncherCaseTests(unittest.TestCase):
    def test_passes_only_for_the_selected_unprivileged_stable_service_image(self):
        with VaultCase() as case:
            status, observations, streams = security.wrong_launcher_memory({'binding': True})
        self.assertEqual(status, 'passed')
        self.assertEqual(streams, {'stdout': 'shell'})
        self.assertEqual((observations['service_pid'], observations['start_time_ticks'], observations['service_uids'],
                          observations['effective_capabilities']), (4242, '777', [999] * 4, 1 << 19))
        self.assertTrue(observations['service_identity_stable'])
        self.assertEqual(observations['refusal'], REFUSED)
        self.assertEqual(observations['engine'], {'memory_read_denied': True})
        stream = case.mocks[1].return_value.__enter__.return_value
        stream.settimeout.assert_called_once_with(5)
        stream.connect.assert_called_once_with(str(case.socket))
        self.assertEqual(case.mocks[5].call_args.args[0][-1], '/proc/4242/exe')
        self.assertEqual(case.mocks[6].call_args.args, ({'binding': True}, 'pw-vault-identity'))

    def test_root_or_caller_credentials_and_restarted_services_fail_closed(self):
        for changed in [dict(uids=(0, 0, 0, 0)), dict(uids=(os.getuid(),) * 4), dict(caps=0), dict(pids=(4242, 4243)),
                        dict(engine_passed=False)]:
            with self.subTest(**{key: str(value) for key, value in changed.items()}), VaultCase(**changed):
                status, _, _ = security.wrong_launcher_memory({})
            self.assertEqual(status, 'failed')

    def test_observer_refusals_name_their_phase_without_prose(self):
        for changed, phase in [(dict(socket_mode=stat.S_IFREG), 'service-socket-ownership'),
                               (dict(image='/usr/bin/pw-vault-identity'), 'service-image')]:
            with self.subTest(phase=phase), VaultCase(**changed):
                with self.assertRaises(ValueError) as caught:
                    security.wrong_launcher_memory({})
            self.assertEqual(caught.exception.observations, {'observer_phase': phase, 'error_type': 'ValueError'})


class SecurityExecuteOutcomeTests(unittest.TestCase):
    def outcome(self, error):
        def probe(_):
            raise error
        with patch.object(security, 'require_owned_guest'), patch.object(security, 'PROBES', {('vault', 'probe'): probe}), \
                patch.object(security, 'retain', side_effect=lambda *args: args) as retained:
            result = security.execute({'case_inputs': {}}, 'vault', 'probe', Path('/evidence'))
        retained.assert_called_once()
        return result[4:]

    def test_case_failures_and_observer_outages_keep_their_typed_observations(self):
        failed = CaseFailed('product refused')
        failed.observations, failed.streams = {'phase': 'gui'}, {'stdout': 'shell'}
        self.assertEqual(self.outcome(failed), ('failed', {'phase': 'gui'}, {'stdout': 'shell'}))
        self.assertEqual(self.outcome(CaseFailed('no facts')), ('failed', {'reason': 'no facts'}, {}))
        outage = OSError('busctl missing')
        outage.observations = {'observer_phase': 'service-owner', 'error_type': 'OSError'}
        self.assertEqual(self.outcome(outage), ('unverified', {'reason': 'native-security-observer-unavailable',
                         'observer_phase': 'service-owner', 'error_type': 'OSError'}, {}))
        self.assertEqual(self.outcome(subprocess.TimeoutExpired('busctl', 10))[0], 'unverified')


if __name__ == '__main__':
    unittest.main()

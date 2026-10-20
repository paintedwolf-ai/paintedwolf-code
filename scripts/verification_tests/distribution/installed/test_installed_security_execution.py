"""Installed security observations must bind typed native facts to product execution."""
import errno
import json
from pathlib import Path
import struct
import threading
import unittest
from unittest.mock import patch

from linux_release.installed_cases import security


class Frame:
    def __init__(self, body):
        self.raw = struct.pack('!I', len(body)) + body
    def sendall(self, raw):
        self.request = raw
    def recv(self, size):
        raw, self.raw = self.raw[:min(size, 3)], self.raw[min(size, 3):]
        return raw


class InstalledSecurityTests(unittest.TestCase):
    def test_refusal_retains_codes_without_identity_or_diagnostic_prose(self):
        frame = Frame(json.dumps({'status': 'refused', 'code': 'executable_mismatch',
                                  'message': 'sensitive diagnostic', 'value': 'never-retain'}).encode())
        result = security.refusal(frame)
        self.assertEqual(result, {'status': 'refused', 'code': 'executable_mismatch',
                                  'identity_returned': True, 'launcher_pid': 0})
        self.assertNotIn('never-retain', json.dumps(result))
        self.assertNotIn('sensitive diagnostic', json.dumps(result))
        self.assertEqual(json.loads(frame.request[4:]), {'op': 'probe'})

    def test_protocol_refuses_truncation_and_oversized_frames(self):
        for raw in [b'', struct.pack('!I', 0), struct.pack('!I', (128 << 10) + 1),
                    struct.pack('!I', 10) + b'{}']:
            frame = Frame(b'')
            frame.raw = raw
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                security.refusal(frame)

    def test_logind_shape_is_validated_before_control_decisions(self):
        row = ['sleep', 'fixture', 'fixture', 'block', 1000, 123]
        with patch.object(security, 'bus_value', return_value=[[row]]):
            self.assertEqual(security.inhibitors(), [row])
        for result in [[], [['invalid']], [[['sleep', '', '', 'block', True, 123]]]]:
            with patch.object(security, 'bus_value', return_value=result), self.assertRaises(ValueError):
                security.inhibitors()

    def test_successful_cli_requires_owned_inhibitor_and_release(self):
        for observed_pid in [123, 456]:
            active = threading.Event()
            observed = threading.Event()
            def product(*args, **kwargs):
                active.set()
                if not observed.wait(5):
                    raise AssertionError("native observer did not sample running product")
                active.clear()
                return {'exit_code': 0, 'timed_out': False, 'output_limit': False,
                        'launch_identity': {'pid': 123, 'start_time_ticks': '42'},
                        'stdout': b'', 'stderr': b'success'}
            def observer():
                if not active.is_set():
                    return []
                row = ['sleep', '', '', 'block', 1000, observed_pid]
                observed.set()
                return [row]
            with self.subTest(observed_pid=observed_pid), patch.object(security, 'run', side_effect=product), \
                    patch.object(security, 'inhibitors', side_effect=observer), patch.object(security.os, 'getuid', return_value=1000):
                status, facts, streams = security.inhibitor_lifetime({})
                self.assertEqual(status, 'passed' if observed_pid == 123 else 'failed')
                self.assertEqual(facts['after'], [])

    def test_memory_observer_never_reads_content_and_refuses_unexpected_errors(self):
        for code in [errno.EPERM, errno.EACCES]:
            with patch.object(security.os, 'open', side_effect=OSError(code, 'fixture')):
                self.assertTrue(security.memory_read_denied(123))
        with patch.object(security.os, 'open', return_value=42) as opened, \
                patch.object(security.os, 'close') as closed:
            self.assertFalse(security.memory_read_denied(123))
            opened.assert_called_once_with('/proc/123/mem', security.os.O_RDONLY)
            closed.assert_called_once_with(42)
        with patch.object(security.os, 'open', side_effect=FileNotFoundError()):
            with self.assertRaises(FileNotFoundError):
                security.memory_read_denied(123)

    def test_missing_actual_application_interaction_cannot_pass_security_case(self):
        with patch.object(security, 'require_owned_guest'), \
                patch.dict(security.PROBES, {('vault', 'wrong-launcher-and-memory'):
                    lambda binding: (_ for _ in ()).throw(security.CaseUnavailable('fixture'))}), \
                patch.object(security, 'retain', side_effect=lambda *args: args):
            result = security.execute({'case_inputs': {}}, 'vault', 'wrong-launcher-and-memory', Path('/unused'))
            self.assertEqual(result[4], 'unverified')
            self.assertEqual(result[5], {'reason': 'installed-app-interaction-unavailable'})

    def test_unsupported_cases_remain_unverified_and_boolean_schema_is_refused(self):
        with patch.object(security, 'require_owned_guest'), patch.object(security, 'retain', side_effect=lambda *args: args):
            result = security.execute({'case_inputs': {}}, 'confinement', 'unsupported-fixture-case', Path('/unused'))
            self.assertEqual(result[4], 'unverified')
            with self.assertRaises(ValueError):
                security.execute({'case_inputs': {'security': {'format_version': True}}},
                                 'vault', 'wrong-launcher-and-memory', Path('/unused'))

    def test_shell_and_engine_hardening_require_same_uid_denial_and_stable_identity(self):
        before = ('42', [1000] * 4, 0)
        for denied, after, expected in [(True, before, True), (False, before, False),
                                        (True, ('43', [1000] * 4, 0), False)]:
            with self.subTest(denied=denied, after=after), patch.object(
                    security, 'process_identity', side_effect=[before, after]), patch.object(
                    security, 'memory_read_denied', return_value=denied), patch.object(security.os, 'getuid', return_value=1000):
                passed, facts = security.protected_process(123, 42)
            self.assertEqual(passed, expected)
            self.assertEqual(facts['pid'], 123)
        for uids, caps in [([0] * 4, 0), ([1000] * 4, 1)]:
            with patch.object(security, 'process_identity', return_value=('42', uids, caps)), patch.object(
                    security, 'memory_read_denied', return_value=True), patch.object(security.os, 'getuid', return_value=1000):
                self.assertFalse(security.protected_process(123, 42)[0])

    def test_security_failure_retains_typed_phase_without_exception_prose(self):
        error = ValueError('do not retain diagnostic text')
        error.observations = {'observer_phase': 'service-image', 'error_type': 'ValueError'}
        with patch.object(security, 'require_owned_guest'), patch.dict(security.PROBES, {
                ('vault', 'wrong-launcher-and-memory'): lambda binding: (_ for _ in ()).throw(error)}), patch.object(
                security, 'retain', side_effect=lambda *args: args):
            result = security.execute({'case_inputs': {}}, 'vault', 'wrong-launcher-and-memory', Path('/unused'))
        self.assertEqual(result[4], 'failed')
        self.assertEqual(result[5]['observer_phase'], 'service-image')
        self.assertNotIn('do not retain', json.dumps(result[5]))

    def test_main_pid_property_is_a_typed_scalar_not_a_method_return_tuple(self):
        unit = '/org/freedesktop/systemd1/unit/owned'
        with patch.object(security, 'bus_value', return_value=[unit]), patch.object(
                security, 'observe', return_value=b'{"type":"u","data":321}'):
            self.assertEqual(security.service_pid(), 321)
        for data in (True, [321], '321'):
            with patch.object(security, 'bus_value', return_value=[unit]), patch.object(
                    security, 'observe', return_value=json.dumps({'type': 'u', 'data': data}).encode()):
                with self.subTest(data=data), self.assertRaises(ValueError):
                    security.service_pid()

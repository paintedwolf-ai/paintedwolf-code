"""Installed security observations must bind typed native facts to product execution."""
import json
from pathlib import Path
import struct
import threading
import time
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
            def product(*args, **kwargs):
                active.set()
                time.sleep(0.25)
                active.clear()
                return {'exit_code': 0, 'timed_out': False, 'output_limit': False,
                        'launch_identity': {'pid': 123, 'start_time_ticks': '42'},
                        'stdout': b'', 'stderr': b'success'}
            def observer():
                return [['sleep', '', '', 'block', 1000, observed_pid]] if active.is_set() else []
            with self.subTest(observed_pid=observed_pid), patch.object(security, 'run', side_effect=product), \
                    patch.object(security, 'inhibitors', side_effect=observer), patch.object(security.os, 'getuid', return_value=1000):
                status, facts, streams = security.inhibitor_lifetime({})
                self.assertEqual(status, 'passed' if observed_pid == 123 else 'failed')
                self.assertEqual(facts['after'], [])

    def test_unsupported_cases_remain_unverified_and_boolean_schema_is_refused(self):
        with patch.object(security, 'require_owned_guest'), patch.object(security, 'retain', side_effect=lambda *args: args):
            result = security.execute({'case_inputs': {}}, 'confinement', 'system-v-ipc-isolation', Path('/unused'))
            self.assertEqual(result[4], 'unverified')
            with self.assertRaises(ValueError):
                security.execute({'case_inputs': {'security': {'format_version': True}}},
                                 'vault', 'wrong-launcher-and-memory', Path('/unused'))

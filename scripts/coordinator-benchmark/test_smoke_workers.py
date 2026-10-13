import json
import pathlib
import sqlite3
import unittest
from unittest.mock import Mock, patch
import yaml
import smoke_workers
from smoke_workers import completion_wake


class CompletionWakeTests(unittest.TestCase):
    def test_scripted_tool_loading_uses_published_schema(self):
        root = pathlib.Path(__file__).resolve().parents[2]
        schema = yaml.safe_load((root / 'lycaon/config/packs/painted-wolf/platform/tools/schemas/request_tools.yaml').read_text())['schema']
        def validate(args):
            self.assertEqual(set(args), set(schema['required']))
            for key, value in args.items():
                self.assertEqual(schema['properties'][key]['type'], 'string')
                self.assertIsInstance(value, str)

        application = Mock()
        application.wait_for.side_effect = [
            {'id': 'first', 'tools': ['request_tools']},
            {'id': 'loaded', 'tools': ['command', 'verify']},
        ]
        calls = [{'name': 'command', 'args': {}}, {'name': 'verify', 'args': {}}]
        with patch.object(smoke_workers, 'terminal', return_value={}):
            smoke_workers.respond(application, 'session', calls)
        loading = application.request.call_args_list[0].args[1]['tool_calls'][0]
        self.assertEqual(loading['name'], 'request_tools')
        validate(loading['args'])
        self.assertEqual(loading['args']['need'], 'command, verify')
        suite = json.loads((root / 'lycaon/test/fixtures/eval/coordinator-benchmark.json').read_text())
        for case in suite['cases']:
            for step in case.get('prelude', []):
                if step['tool'] == 'request_tools':
                    with self.subTest(case=case['id']):
                        validate(step['args'])

    def test_only_new_expected_host_notifications_allow_another_closeout(self):
        allowed = {('host_kick', 'process.finished'), ('host_loop_wake', 'loop.wake')}
        with sqlite3.connect(':memory:') as db:
            db.execute('CREATE TABLE messages(session_id,ord,role,origin,kind,host_signal_id)')
            def append(order, origin, kind, signal='', role='user'):
                db.execute('INSERT INTO messages VALUES(?,?,?,?,?,?)', ('session', order, role, origin, kind, signal))
            append(1, 'host', 'host_loop_wake', 'loop.wake')
            self.assertFalse(completion_wake(db, 'session', allowed))
            append(2, 'model', 'completion_report', role='assistant')
            self.assertFalse(completion_wake(db, 'session', allowed))
            append(3, 'host', 'host_kick', 'process.finished')
            append(4, 'host', 'host_loop_wake', 'loop.wake')
            self.assertTrue(completion_wake(db, 'session', allowed))
            self.assertFalse(completion_wake(db, 'session', set()))
            for origin, kind, signal in [('host', 'host_kick', 'verification.required'),
                                         ('model', 'host_loop_wake', 'loop.wake'),
                                         ('host', 'workflow_boundary', '')]:
                with self.subTest(origin=origin, kind=kind, signal=signal):
                    append(5, origin, kind, signal)
                    self.assertFalse(completion_wake(db, 'session', allowed))
                    db.execute('DELETE FROM messages WHERE ord=5')
            append(5, 'model', 'completion_report', role='assistant')
            self.assertFalse(completion_wake(db, 'session', allowed))

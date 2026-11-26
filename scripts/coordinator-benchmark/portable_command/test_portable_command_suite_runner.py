"""Unit tests for the Portable Command Q01-Q22 suite runner and command envelope probe."""
import io
import json
import pathlib
import sqlite3
import tempfile
import unittest
from unittest.mock import patch
from portable_command import cli, suite_runner
from portable_command.test_portable_command_harness import FakeApp, FakeSSE


class ProbeFakeApp(FakeApp):
    """Fake app with configurable offered command tool schema and envelope behavior."""
    def __init__(self, directory, *, schema_props, native_progress=False, reject=False, offer_command=True):
        super().__init__(directory, mode='publish')
        self.schema_props = schema_props
        self.native_progress = native_progress
        self.reject = reject
        self.offer_command = offer_command
        self.recorded_calls = []

    def request(self, path, body=None, missing=False, method=None):
        if path.endswith('/prompts'):
            tools = ['command', 'update_progress'] if self.offer_command else ['update_progress']
            schemas = [{'name': 'command', 'description': 'Run command', 'schema': {'type': 'object', 'properties': self.schema_props}}] if self.offer_command else []
            self.pending_req = {'pending': True, 'id': 'req-1', 'session_id': self.session_id, 'tools': tools, 'tool_schemas': schemas}
            return {'operation_id': body['operation_id']}
        return super().request(path, body=body, missing=missing, method=method)

    def _handle_tool_calls(self, calls):
        self.recorded_calls.extend(calls)
        db = sqlite3.connect(self.directory / 'store.db')
        for idx, tc in enumerate(calls, start=1):
            cid = tc['id']
            if self.reject:
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command','processes','rejected',0)", ('rcpt-cmd', self.session_id, cid))
                res = {'outcome': 'rejected', 'codes': ['COMMAND_ARGS_INVALID'], 'content': json.dumps({'refusal': {'code': 'COMMAND_ARGS_INVALID'}})}
            else:
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command','processes','completed',1)", ('rcpt-cmd', self.session_id, cid))
                if self.native_progress:
                    db.execute("INSERT INTO invocation_receipts VALUES ('rcpt-prog',?,?, 'update_progress','progress','completed',1)", (self.session_id, cid))
                res = {'outcome': 'completed', 'content': '[command#1]\n' + json.dumps({'status': 0, 'state': 'completed'})}
            db.execute("INSERT INTO messages VALUES (?,?,?,'tool',?,?)", (f'msg-{cid}', self.session_id, idx, cid, json.dumps(res)))
        db.commit()
        db.close()


class PortableCommandSuiteRunnerTests(unittest.TestCase):
    def test_probe_command_envelope_reports_schema_and_never_uses_sibling_fallback(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            with patch.object(suite_runner, 'checkpoint_closed_store', return_value={}):
                app_no_prop = []
                def make_no_prop(d):
                    a = ProbeFakeApp(d, schema_props={'command': {'type': 'string'}}, native_progress=False, reject=False)
                    app_no_prop.append(a)
                    return a
                r1 = suite_runner.probe_command_envelope(root / 'p1', app_factory=make_no_prop, sse_factory=FakeSSE)
                r2 = suite_runner.probe_command_envelope(
                    root / 'p2',
                    app_factory=lambda d: ProbeFakeApp(d, schema_props={'command': {'type': 'string'}, 'progress_update': {'type': 'object'}}, native_progress=True),
                    sse_factory=FakeSSE,
                )
                r3 = suite_runner.probe_command_envelope(
                    root / 'p3',
                    app_factory=lambda d: ProbeFakeApp(d, schema_props={}, offer_command=False),
                    sse_factory=FakeSSE,
                )
            self.assertFalse(r1['schema_has_progress_update'])
            self.assertEqual(r1['envelope_status'], 'accepted_without_progress_receipt')
            self.assertFalse(r1['native_envelope_qualified'])
            self.assertFalse(r1['sibling_fallback_used'])
            self.assertEqual([c['name'] for c in app_no_prop[0].recorded_calls], ['command'])
            self.assertIn('progress_update', app_no_prop[0].recorded_calls[0]['args'])
            self.assertTrue((root / 'p1' / 'envelope-probe.json').is_file())

            self.assertTrue(r2['schema_has_progress_update'])
            self.assertEqual(r2['envelope_status'], 'supported')
            self.assertTrue(r2['native_envelope_qualified'])

            self.assertEqual(r3['envelope_status'], 'unavailable')
            self.assertFalse(r3['native_envelope_qualified'])

    def test_run_suite_records_all_cases_and_isolates_failures(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            seen = []
            def fake_case_runner(case, out_dir, *, export_dir=None, app_factory=None, sse_factory=None):
                seen.append((case['id'], pathlib.Path(out_dir).name))
                if case['id'] == 'draft-read-transform':
                    raise RuntimeError('simulated draft setup failure')
                passed = case['id'] == 'first-mutation'
                return {
                    'case_id': case['id'],
                    'session_id': f"sess-{case['id']}",
                    'grade': {'qualification': 'native-runtime', 'passed': passed, 'dimensions': {'artifacts': passed, 'execution': True}},
                    'audit': {'observation_gaps': ['profile_binding_approximate'] if case['profile'] != 'implement' else []},
                }

            report = suite_runner.run_suite(
                root / 'suite',
                case_ids=['first-mutation', 'draft-read-transform', 'read-only-static-refusal'],
                case_runner=fake_case_runner,
            )
            self.assertEqual(len(seen), 3)
            self.assertEqual(report['summary']['total'], 3)
            self.assertEqual(report['summary']['measured'], 2)
            self.assertEqual(report['summary']['unmeasured'], 1)
            self.assertEqual(report['summary']['passed'], 1)
            self.assertEqual(report['summary']['failed'], 2)
            by_id = {c['case_id']: c for c in report['cases']}
            self.assertEqual(by_id['first-mutation']['envelope_mode'], 'sibling_update_progress')
            self.assertFalse(by_id['first-mutation']['native_envelope_qualified'])
            self.assertEqual(by_id['draft-read-transform']['attempt'], 'unmeasured')
            self.assertEqual(by_id['draft-read-transform']['qualification'], 'unqualified')
            self.assertIn('simulated draft setup failure', by_id['draft-read-transform']['error'])
            self.assertFalse(by_id['read-only-static-refusal']['profile_binding']['exact_match'])
            self.assertEqual(by_id['read-only-static-refusal']['profile_binding']['posture'], 'vet')
            saved = json.loads((root / 'suite' / 'suite-report.json').read_text())
            self.assertEqual(saved['summary']['total'], 3)

            with self.assertRaises(ValueError):
                suite_runner.run_suite(root / 'bad', case_ids=['does-not-exist'], case_runner=fake_case_runner)

    def test_cli_suite_and_probe_envelope_modes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            with patch('sys.stdout', new=io.StringIO()):
                rc_probe = cli.main(
                    ['--probe-envelope', '--out', str(root / 'probe')],
                    envelope_prober=lambda out: {
                        'session_id': 's-probe', 'schema_has_progress_update': False,
                        'command_schema_properties': ['command'], 'envelope_status': 'rejected',
                        'native_envelope_qualified': False, 'sibling_fallback_used': False,
                    },
                )
                rc_suite_ok = cli.main(
                    ['--suite', '--cases', 'first-mutation,native-option-no-fallback', '--out', str(root / 's1')],
                    suite_runner=lambda out, case_ids=None, export_dir=None: {'summary': {'total': len(case_ids), 'failed': 0}},
                )
                rc_suite_fail = cli.main(
                    ['--suite', '--out', str(root / 's2')],
                    suite_runner=lambda out, case_ids=None, export_dir=None: {'summary': {'total': 40, 'failed': 3}},
                )
            self.assertEqual(rc_probe, 0)
            self.assertEqual(rc_suite_ok, 0)
            self.assertEqual(rc_suite_fail, 1)

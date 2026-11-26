"""Unit tests for the zero-model Portable Command harness, observation adapter, and CLI."""
import io
import json
import pathlib
import sqlite3
import tempfile
import unittest
from unittest.mock import patch
from portable_command import cli, harness, observe
from portable_command.contracts import load_suite

CASES = {c['id']: c for c in load_suite()['cases']}


def create_audit_db(db_path):
    db = sqlite3.connect(db_path)
    db.executescript('''
        CREATE TABLE invocation_receipts (id TEXT PRIMARY KEY, session_id TEXT, tool_call_id TEXT, tool_name TEXT, owner TEXT, status TEXT, invoked INTEGER);
        CREATE TABLE invocation_programs (id TEXT PRIMARY KEY, parent_receipt_id TEXT, state TEXT, status_code INTEGER, stop_code TEXT);
        CREATE TABLE invocation_program_executions (id TEXT PRIMARY KEY, program_id TEXT, ordinal INTEGER, operation_id TEXT, native_command TEXT, owner TEXT, state TEXT, effect_certainty TEXT, accepted_revision TEXT, published_revision TEXT, native_gap_json TEXT);
        CREATE TABLE invocation_program_revision_pins (program_id TEXT, execution_id TEXT, ordinal INTEGER, role TEXT, mode TEXT, path TEXT);
        CREATE TABLE invocation_program_outputs (id TEXT PRIMARY KEY, program_id TEXT, execution_id TEXT, stream TEXT, reference TEXT, complete INTEGER, redacted INTEGER, preview TEXT, preview_truncated INTEGER, line_count INTEGER, inline_digest TEXT, blob_id TEXT);
        CREATE TABLE invocation_program_publications (program_id TEXT PRIMARY KEY, session_id TEXT, state TEXT, materialized INTEGER);
        CREATE TABLE source_command_windows (id TEXT PRIMARY KEY, session_id TEXT, tool_call_id TEXT, state TEXT);
        CREATE TABLE messages (id TEXT PRIMARY KEY, session_id TEXT, ord INTEGER, role TEXT, tool_result_call_id TEXT, tool_result_json TEXT);
    ''')
    db.commit()
    db.close()


class FakeSSE:
    def __init__(self, url, token, out_path):
        self.out_path = pathlib.Path(out_path)
        self.closed = False

    def start(self):
        self.out_path.write_text('{"topic":"activity"}\n')

    def close(self):
        self.closed = True


class FakeApp:
    """In-process fake matching smoke.Application for deterministic unit & defect-control coverage."""
    def __init__(self, directory, mode='publish'):
        self.directory = pathlib.Path(directory)
        self.directory.mkdir(parents=True, exist_ok=True)
        self.url = 'http://127.0.0.1:1'
        self.token = 'tok'
        self.mode = mode
        self.process = None
        self.closed = False
        self.project_root = None
        self.session_id = 'sess1234-0000-0000-0000-000000000001'
        self.turn = 0
        self.pending_req = None
        if mode != 'missing-log':
            log_line = 'level=INFO msg="startup"\n'
            if mode == 'external-launch':
                log_line += 'level=INFO msg="process boundary selected" tool=command session_id=sess1234…\n'
            (self.directory / 'sidecar.log').write_text(log_line)
        create_audit_db(self.directory / 'store.db')

    def ready(self):
        return None

    def close(self, cleanup=True):
        self.closed = True

    def wait_for(self, fn, phase, session=None, timeout=10):
        res = fn()
        if not res:
            raise RuntimeError('fake timeout: ' + phase)
        return res

    def request(self, path, body=None, missing=False, method=None):
        if path == '/v1/projects':
            self.project_root = pathlib.Path(body['roots'][0]['path'])
            return {'id': 'proj-1'}
        if path == '/v1/sessions':
            return {'id': self.session_id}
        if path == f'/v1/sessions/{self.session_id}':
            return {'id': self.session_id, 'status': 'idle'}
        if path.endswith('/prompts'):
            self.pending_req = {'pending': True, 'id': 'req-1', 'session_id': self.session_id, 'tools': ['command', 'update_progress']}
            return {'operation_id': body['operation_id']}
        if path == '/harness/llm/pending':
            return self.pending_req or {'pending': False}
        if path == '/harness/llm/respond':
            self._handle_tool_calls(body['tool_calls'])
            self.turn += 1
            self.pending_req = {'pending': True, 'id': f'req-{self.turn+1}', 'session_id': self.session_id, 'tools': ['command', 'update_progress']}
            return {'ok': True}
        if path.endswith('/invocations'):
            return {'invocations': []}
        if path.endswith('/messages'):
            return {'messages': []}
        return {}

    def _handle_tool_calls(self, calls):
        db = sqlite3.connect(self.directory / 'store.db')
        for idx, tc in enumerate(calls, start=1):
            cid, name = tc['id'], tc['name']
            if name == 'update_progress':
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'update_progress','progress','completed',1)", (f'rcpt-{cid}', self.session_id, cid))
                db.execute("INSERT INTO messages VALUES (?,?,?,'tool',?,?)", (f'msg-{cid}', self.session_id, self.turn * 10 + idx, cid, json.dumps({'outcome': 'completed', 'content': '{}'})))
            elif name == 'command' and self.mode in {'publish', 'missing-pub'}:
                (self.project_root / 'result.txt').write_bytes(b'created\n')
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command','processes','completed',1)", ('rcpt-cmd', self.session_id, cid))
                db.execute("INSERT INTO invocation_programs VALUES ('prog-1','rcpt-cmd','completed',0,'')")
                db.execute("INSERT INTO invocation_program_executions VALUES ('ex-1','prog-1',1,'printf','printf','processes','completed','none','','','')")
                pub_rev = 'rev-1' if self.mode == 'publish' else ''
                db.execute("INSERT INTO invocation_program_executions VALUES ('ex-2','prog-1',2,'write','>','filesystem','completed','completed','',?,'')", (pub_rev,))
                db.execute("INSERT INTO invocation_program_revision_pins VALUES ('prog-1','ex-2',0,'destination','absent','result.txt')")
                if self.mode == 'publish':
                    db.execute("INSERT INTO invocation_program_publications VALUES ('prog-1',?,'committed',1)", (self.session_id,))
                payload = {'program_id': 'prog-1', 'state': 'completed', 'status': 0, 'stdout': '', 'stderr': '', 'completed_effects': 1, 'uncertain_effects': 0, 'native_gaps': 0}
                res = {'outcome': 'completed', 'content': '[command#1]\n' + json.dumps(payload)}
                db.execute("INSERT INTO messages VALUES (?,?,?,'tool',?,?)", (f'msg-{cid}', self.session_id, self.turn * 10 + idx, cid, json.dumps(res)))
            elif name == 'command':
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command','processes','rejected',0)", ('rcpt-cmd', self.session_id, cid))
                db.execute("INSERT INTO invocation_programs VALUES ('prog-1','rcpt-cmd','rejected',2,'COMMAND_OPTION_UNSUPPORTED')")
                payload = {
                    'program_id': 'prog-1', 'state': 'rejected', 'status': 2, 'stop_code': 'COMMAND_OPTION_UNSUPPORTED',
                    'stdout': '', 'stderr': '', 'completed_effects': 0, 'uncertain_effects': 0, 'native_gaps': 0, 'replay_available': True,
                    'refusal': {
                        'code': 'COMMAND_OPTION_UNSUPPORTED',
                        'feedback': 'Usage: cat [-n] [FILE...]',
                        'details': {'source_start_byte': 4, 'source_end_byte': 28, 'expected_form': 'cat [-n] [FILE...]'},
                    },
                }
                res = {'outcome': 'rejected', 'codes': ['COMMAND_OPTION_UNSUPPORTED'], 'content': '[command#1]\n' + json.dumps(payload)}
                db.execute("INSERT INTO messages VALUES (?,?,?,'tool',?,?)", (f'msg-{cid}', self.session_id, self.turn * 10 + idx, cid, json.dumps(res)))
        db.commit()
        db.close()


class PortableCommandHarnessTests(unittest.TestCase):
    def test_known_publication_run_export_and_validated_replay(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            with patch.object(harness, 'checkpoint_closed_store', return_value={}):
                res = harness.run_case(
                    CASES['first-mutation'], root / 'run1', export_dir=root / 'export',
                    app_factory=lambda d: FakeApp(d, mode='publish'), sse_factory=FakeSSE,
                )
            self.assertTrue(res['grade']['passed'])
            bundle_path = root / 'export' / 'sanitized-fixture.json'
            self.assertEqual(harness.resolve_case(str(bundle_path))['id'], 'first-mutation')
            # Tampered replay bundle must be rejected before launch.
            tampered = json.loads(bundle_path.read_text())
            tampered['turns'][0]['program'] = 'printf hacked > ./result.txt'
            bad_path = root / 'tampered.json'
            bad_path.write_text(json.dumps(tampered))
            with self.assertRaises(ValueError):
                harness.resolve_case(str(bad_path))

    def test_missing_publication_control_fails_receipts_and_authority(self):
        with tempfile.TemporaryDirectory() as tmp:
            with patch.object(harness, 'checkpoint_closed_store', return_value={}):
                res = harness.run_case(
                    CASES['first-mutation'], pathlib.Path(tmp) / 'run',
                    app_factory=lambda d: FakeApp(d, mode='missing-pub'), sse_factory=FakeSSE,
                )
            self.assertFalse(res['grade']['dimensions']['receipts'])
            self.assertFalse(res['grade']['dimensions']['authority'])

    def test_refusal_positive_external_launch_and_missing_observer_controls(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            with patch.object(harness, 'checkpoint_closed_store', return_value={}):
                clean = harness.run_case(CASES['native-option-no-fallback'], root / 'clean', app_factory=lambda d: FakeApp(d, mode='refuse'), sse_factory=FakeSSE)
                launched = harness.run_case(CASES['native-option-no-fallback'], root / 'launched', app_factory=lambda d: FakeApp(d, mode='external-launch'), sse_factory=FakeSSE)
                missing = harness.run_case(CASES['native-option-no-fallback'], root / 'missing', app_factory=lambda d: FakeApp(d, mode='missing-log'), sse_factory=FakeSSE)
            self.assertTrue(clean['observation']['facts']['no_external_launch'])
            self.assertEqual(len(clean['audit']['prelude']), 1)
            self.assertFalse(launched['observation']['facts']['no_external_launch'])
            self.assertEqual(launched['audit']['external_launch_audit']['sidecar']['command_launches'], 1)
            self.assertFalse(missing['observation']['facts']['no_external_launch'])
            self.assertIn('missing_sidecar_log', missing['audit']['observation_gaps'])

    def test_cli_modes_and_unknown_case(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            fake_result = {'case_id': 'first-mutation', 'session_id': 's1', 'grade': {'qualification': 'native-runtime', 'passed': True, 'dimensions': {'artifacts': True}}}
            with patch('sys.stdout', new=io.StringIO()):
                self.assertEqual(cli.main(['--case', 'first-mutation', '--out', str(root / 'o1')], runner=lambda c, o, export_dir=None: fake_result), 0)
                self.assertEqual(cli.main(['--replay', 'first-mutation', '--out', str(root / 'o2')], runner=lambda c, o, export_dir=None: {**fake_result, 'grade': {**fake_result['grade'], 'passed': False}}), 1)
            with self.assertRaises(ValueError):
                harness.resolve_case('nonexistent-case-id')

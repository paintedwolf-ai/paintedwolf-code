"""Unit controls for real-shaped chunked command_output awaiting, cursor continuity, and gaps."""
import json
import pathlib
import sqlite3
import tempfile
import unittest
from unittest.mock import patch
from portable_command import harness, reporting
from portable_command.contracts import load_suite
from portable_command.test_portable_command_harness import FakeApp, FakeSSE

CASES = {c['id']: c for c in load_suite()['cases']}


class ChunkedOutputFakeApp(FakeApp):
    """Fake app emitting real commandOutput JSON shape (string cursors + chunks array)."""
    def __init__(self, directory, *, pages, reject_output=False):
        super().__init__(directory, mode='publish')
        self.pages = pages
        self.reject_output = reject_output
        self.requested_cursors = []

    def request(self, path, body=None, missing=False, method=None):
        res = super().request(path, body=body, missing=missing, method=method)
        if path == '/harness/llm/pending' and isinstance(res, dict) and res.get('pending'):
            res['tools'] = ['command', 'update_progress', 'wait', 'command_output']
        return res

    def _handle_tool_calls(self, calls):
        db = sqlite3.connect(self.directory / 'store.db')
        for idx, tc in enumerate(calls, start=1):
            cid, name = tc['id'], tc['name']
            if name == 'command':
                db.execute("INSERT INTO invocation_receipts VALUES ('rcpt-cmd',?,?,'command','processes','running',1)", (self.session_id, cid))
                db.execute("INSERT INTO invocation_programs VALUES ('prog-1','rcpt-cmd','running',NULL,'')")
                res = {'outcome': 'completed', 'content': '[command#1]\n' + json.dumps({'program_id': 'prog-1', 'state': 'running', 'handle': 'command-1'})}
            elif name == 'wait':
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'wait','processes','completed',1)", (f'rcpt-{cid}', self.session_id, cid))
                res = {'outcome': 'completed', 'content': json.dumps({'outcome': 'process_done'})}
            elif name == 'command_output':
                cur = (tc.get('args') or {}).get('cursor', 0)
                self.requested_cursors.append(cur)
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command_output','processes','completed',1)", (f'rcpt-{cid}', self.session_id, cid))
                if self.reject_output:
                    res = {'outcome': 'rejected', 'codes': ['BACKGROUND_HANDLE_NOT_FOUND'], 'content': '{}'}
                else:
                    page = self.pages[min(len(self.requested_cursors) - 1, len(self.pages) - 1)]
                    if page.get('running') is False:
                        db.execute("UPDATE invocation_programs SET state='completed', status_code=? WHERE id='prog-1'", (page.get('exit_code', 0),))
                    res = {'outcome': 'completed', 'content': json.dumps(page)}
            else:
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'update_progress','progress','completed',1)", (f'rcpt-{cid}', self.session_id, cid))
                res = {'outcome': 'completed', 'content': '{}'}
            db.execute("INSERT INTO messages VALUES (?,?,?,'tool',?,?)", (f'msg-{cid}', self.session_id, self.turn * 10 + idx, cid, json.dumps(res)))
        db.commit()
        db.close()


class PortableCommandHandleAwaitTests(unittest.TestCase):
    def test_multipage_command_output_assembles_stdout_stderr_in_cursor_order(self):
        pages = [
            {
                'handle': 'command-1', 'cursor': '0', 'next_cursor': '2', 'running': True,
                'chunks': [{'cursor': 0, 'stream': 'stdout', 'text': '100'}, {'cursor': 1, 'stream': 'stderr', 'text': 'warn\n'}],
            },
            {
                'handle': 'command-1', 'cursor': '2', 'next_cursor': '3', 'running': False, 'exit_code': 0,
                'chunks': [{'cursor': 2, 'stream': 'stdout', 'text': '00\n'}],
            },
            {
                'handle': 'command-1', 'cursor': '3', 'next_cursor': '3', 'running': False, 'exit_code': 0,
                'chunks': [],
            },
        ]
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            app_holder = []
            def factory(d):
                a = ChunkedOutputFakeApp(d, pages=pages)
                app_holder.append(a)
                return a
            with patch.object(harness, 'checkpoint_closed_store', return_value={}):
                res = harness.run_case(CASES['literal-null-sink'], root / 'run', app_factory=factory, sse_factory=FakeSSE)
            self.assertEqual(app_holder[0].requested_cursors, [0, 2, 3])
            self.assertEqual(res['observation']['program']['stdout'], '10000\n')
            self.assertEqual(res['observation']['program']['stderr'], 'warn\n')
            self.assertEqual(res['observation']['program']['status'], 0)
            self.assertTrue(res['observation']['program']['complete'])
            self.assertEqual(res['audit']['observation_gaps'], [])

    def test_truncation_cursor_gap_and_read_rejection_surface_explicit_gaps(self):
        cases = [
            ('truncated', [{'handle': 'command-1', 'cursor': '0', 'next_cursor': '1', 'running': False, 'exit_code': 0, 'truncated': True, 'chunks': [{'cursor': 0, 'stream': 'stdout', 'text': 'x'}]}], False, 'command_output_truncated'),
            ('cursor_gap', [{'handle': 'command-1', 'cursor': '0', 'next_cursor': '5', 'running': False, 'exit_code': 0, 'chunks': [{'cursor': 2, 'stream': 'stdout', 'text': 'skip'}]}], False, 'command_output_cursor_gap'),
            ('handle_mismatch', [{'handle': 'other-9', 'cursor': '0', 'next_cursor': '0', 'running': False, 'exit_code': 0, 'chunks': []}], False, 'command_output_cursor_gap'),
            ('read_rejected', [], True, 'command_output_rejected'),
        ]
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            for label, pages, rej, expected_gap in cases:
                with self.subTest(defect=label), patch.object(harness, 'checkpoint_closed_store', return_value={}):
                    res = harness.run_case(CASES['literal-null-sink'], root / label, app_factory=lambda d, p=pages, r=rej: ChunkedOutputFakeApp(d, pages=p, reject_output=r), sse_factory=FakeSSE)
                    self.assertIn(expected_gap, res['audit']['observation_gaps'])
                    disp = reporting.classify_case_disposition({'attempt': 'measured', 'qualification': 'native-runtime', 'passed': True, 'observation_gaps': res['audit']['observation_gaps']})
                    self.assertEqual(disp['disposition'], 'unqualified')
                    self.assertEqual(disp['discrepancy_category'], 'missing_observation')

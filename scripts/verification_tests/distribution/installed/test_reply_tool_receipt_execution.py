"""Real SQLite projection requires durable successful invoked acquisition and command."""
from contextlib import nullcontext
import json
import sqlite3
import unittest
from unittest.mock import patch

from linux_release.installed_cases import fixture_receipt as owner
from linux_release.installed_cases.profile_observer import schema

SESSION = '10000000-0000-4000-8000-000000000001'
MESSAGE = '10000000-0000-4000-8000-000000000002'
RECEIPT = '10000000-0000-4000-8000-000000000003'
PROJECT = '10000000-0000-4000-8000-000000000004'


class ReplyToolReceiptTests(unittest.TestCase):
    def fixture(self, tool='request_tools', *, target='command'):
        call, subsystem, args = (('call_1234abcd', 'tool_surface', {'need': target}) if tool == 'request_tools'
                                else ('call_abcd1234', 'processes', {'command': '/usr/bin/true'}) if target == 'command'
                                else ('call_abcd1234', 'browser', {'project_dir': '.', 'path': 'fixture.html'}))
        return {'tool': tool, 'tool_call_id': call, 'outcome': 'completed', 'tool_args': args,
            'content': json.dumps({'need': target, 'loaded': [target]}) if tool == 'request_tools' else '[page#1]\ncompacted opaque result',
            'invocation': {'id': RECEIPT, 'tool': tool, 'tool_call_id': call,
                           'owner': subsystem, 'status': 'completed', 'invoked': True}}

    def observe(self, result, *, tool='request_tools', target='command', duplicate=False, mutate=None, body=None, during=None):
        with sqlite3.connect(':memory:') as database:
            database.executescript('PRAGMA user_version=5; CREATE TABLE projects(id TEXT); '
                'CREATE TABLE project_roots(id TEXT,project_id TEXT,path TEXT); CREATE TABLE messages(id TEXT,session_id TEXT,role TEXT,tool_result_json TEXT,evidence_handles_json TEXT); '
                'CREATE TABLE sessions(id TEXT,project_id TEXT,status TEXT,provider_id TEXT,model TEXT); '
                'CREATE TABLE evidence_records(project_id TEXT,session_id TEXT,handle TEXT,kind TEXT,shape TEXT,fidelity TEXT,source_tool TEXT,truncated INTEGER,superseded_by TEXT,content_blob_sha256 TEXT); '
                'CREATE TABLE content_blob_objects(project_id TEXT,sha256 TEXT,byte_size INTEGER,stored_size INTEGER);')
            database.execute('INSERT INTO sessions(id,project_id) VALUES(?,?)', (SESSION, PROJECT))
            database.execute('INSERT INTO evidence_records VALUES(?,?,?,?,?,?,?,?,?,?)',
                (PROJECT, SESSION, 'page#1', 'page', 'surface_snapshot', 'structured', 'page_open', 0, None, 'a'*64))
            database.execute('INSERT INTO content_blob_objects VALUES(?,?,?,?)', (PROJECT, 'a'*64, 100, 50))
            database.execute('INSERT INTO messages VALUES(?,?,?,?,?)',
                             (MESSAGE, SESSION, 'tool', json.dumps(result), '["page#1"]'))
            if duplicate:
                database.execute('INSERT INTO messages VALUES(?,?,?,?,?)',
                                 (RECEIPT, SESSION, 'tool', json.dumps(result), '["page#1"]'))
            if mutate is not None:
                mutate(database)
            def read(*args, **kwargs):
                if during is not None:
                    during(database)
                return body if body is not None else json.dumps([json.dumps({'id': 'page_owned', 'live_pages': ['page_owned'], 'max_pages': 4})]).encode()
            def selected_database(app, *, required=None):
                schema(database, required)
                return nullcontext(database)
            with patch.object(owner, '_database', side_effect=selected_database), patch.object(
                    owner, 'read_object', side_effect=read):
                return owner.committed_fixture_tool(None, PROJECT, SESSION, tool, 'call_1234abcd' if tool == 'request_tools' else 'call_abcd1234', target=target)

    def test_actual_successful_receipts_and_fixed_args_are_required(self):
        for tool in ('request_tools', 'command'):
            found = self.observe(self.fixture(tool), tool=tool)
            self.assertEqual(found['invocation_id'], RECEIPT)
            self.assertEqual(found['message_id'], MESSAGE)
            self.assertIs(found['invoked'], True)
        for field, value in [('owner', 'foreign'), ('status', 'rejected'), ('status', 'deferred'),
                             ('invoked', False), ('invoked', 1), ('tool_call_id', 'foreign'),
                             ('tool', 'command'), ('id', 'noncanonical')]:
            with self.subTest(field=field, value=value):
                result = self.fixture()
                result['invocation'][field] = value
                with self.assertRaises(ValueError):
                    self.observe(result)
        for change in ({'tool_args': {'need': 'read'}}, {'outcome': 'rejected'},
                       {'content': json.dumps({'need': 'command', 'loaded': []})}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                self.observe({**self.fixture(), **change})

    def test_ambiguous_wrong_call_or_oversized_rows_never_certify_execution(self):
        with self.assertRaises(ValueError):
            self.observe(self.fixture(), duplicate=True)
        self.assertIsNone(self.observe({**self.fixture(), 'tool_call_id': 'foreign'}))
        result = self.fixture()
        result['padding'] = 'x' * (1 << 20)
        with self.assertRaises(ValueError):
            self.observe(result)

    def test_page_open_shares_call_identity_guards_but_keeps_browser_arguments_and_owner(self):
        for tool in ('request_tools', 'page_open'):
            result = self.observe(self.fixture(tool, target='page_open'), tool=tool, target='page_open')
            self.assertEqual(result['tool'], tool)
            self.assertEqual(result['owner'], 'tool_surface' if tool == 'request_tools' else 'browser')
            self.assertEqual(result['arguments'], {'need': 'page_open'} if tool == 'request_tools' else {'project_dir': '.', 'path': 'fixture.html'})
            if tool == 'page_open':
                self.assertEqual(result['payload']['id'], 'page_owned')
        for change in ({'tool_args': {'path': 'foreign.html'}}, {'tool_args': {'path': 'fixture.html'}},
                       {'tool_args': {'project_dir': '/tmp', 'path': 'fixture.html'}}, {'tool_args': {'command': '/usr/bin/true'}},
                       {'invocation': {**self.fixture('page_open', target='page_open')['invocation'], 'code': 'REFUSED'}}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                self.observe({**self.fixture('page_open', target='page_open'), **change}, tool='page_open', target='page_open')
        with self.assertRaises(ValueError):
            self.observe(self.fixture('page_open', target='page_open'), tool='page_open', target='command')

    def test_original_page_body_requires_exact_message_ledger_and_blob_binding(self):
        changes = [
            ("UPDATE messages SET evidence_handles_json=?", ('["page#2"]',)),
            ("UPDATE messages SET evidence_handles_json=?", ('["page#1","page#1"]',)),
            ("UPDATE evidence_records SET project_id=?", (SESSION,)),
            ("UPDATE evidence_records SET source_tool=?", ('capture_page',)),
            ("UPDATE evidence_records SET truncated=?", (1,)),
            ("UPDATE evidence_records SET superseded_by=?", ('page#2',)),
            ("UPDATE evidence_records SET shape=?", ('command',)),
            ("DELETE FROM content_blob_objects", ()),
            ("ALTER TABLE evidence_records RENAME COLUMN source_tool TO foreign_field", ()),
        ]
        result = self.fixture('page_open', target='page_open')
        for query, args in changes:
            with self.subTest(query=query), self.assertRaises(ValueError):
                self.observe(result, tool='page_open', target='page_open',
                    mutate=lambda db: db.execute(query, args))
        for body in (b'[]', b'["{}"]', b'["{}","{}"]', b'[true]', b'["not JSON"]'):
            with self.subTest(body=body), self.assertRaises(ValueError):
                self.observe(result, tool='page_open', target='page_open', body=body)
        with self.assertRaisesRegex(ValueError, 'changed'):
            self.observe(result, tool='page_open', target='page_open',
                during=lambda db: db.execute("UPDATE content_blob_objects SET stored_size=49"))
        with self.assertRaises(ValueError):
            self.observe(result, tool='page_open', target='page_open',
                during=lambda db: db.execute("UPDATE messages SET tool_result_json='{}'"))

    def test_page_evidence_retains_only_the_closed_live_page_fields(self):
        document = {'id': 'page_owned', 'live_pages': ['page_owned'], 'max_pages': 4,
                    'final_url': 'PRIVATE URL', 'unknown': 'PRIVATE EXTRA'}
        found = self.observe(self.fixture('page_open', target='page_open'), tool='page_open', target='page_open',
            body=json.dumps([json.dumps(document)]).encode())
        self.assertEqual(found['payload'], {key: document[key] for key in ('id', 'live_pages', 'max_pages')})
        self.assertNotIn('PRIVATE', json.dumps(found))

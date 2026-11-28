"""Exercise replay witness drivers against isolated protocol and ledger fixtures."""
import contextlib
import http.client
import pathlib
import sqlite3
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from portable_command import cancel_witness, lifecycle_oracles, live_witness_driver as live
from portable_command.test_portable_command_harness import create_audit_db


class LiveWitnessDriverTests(unittest.TestCase):
    def context(self, case_id, root, send_calls=None):
        def wait_for(check, reason, **_):
            self.assertTrue(check(), reason)

        prelude = []
        witness = live.LiveWitnessContext(
            {'id': case_id}, SimpleNamespace(wait_for=wait_for), 'session', 'project', 'root',
            root, root, 'surface', send_calls, prelude,
        )
        self.addCleanup(witness.cleanup)
        return witness, prelude

    def test_stale_approval_replaces_revision_before_resolving_checkpoint(self):
        with tempfile.TemporaryDirectory() as directory:
            witness, _ = self.context('stale-approval', directory)
            resolved = threading.Event()
            steps = []
            options = [
                {'kind': 'current_action', 'decision_action': 'approve', 'id': 'once'},
                {'kind': 'lease', 'rung': 'chat', 'decision_action': 'approve', 'id': 'chat'},
            ]

            def respond(_, method, path, payload=None):
                if method == 'GET':
                    return {'checkpoints': [{'id': 'old', 'resolved_at': 'earlier'}, {
                        'id': 'current', 'kind': 'tool_approval', 'approval_plan': {'options': options},
                    }]}
                if path.endswith('/editor-documents'):
                    return {'id': 'doc', 'revision': len(steps) + 1, 'content': 'human\n'}
                if method == 'PUT':
                    steps.append(('edit', payload['expected_revision'], payload['content']))
                    return {'revision': 2}
                steps.append(('resolve', path, payload['option_id']))
                resolved.set()
                return None

            with patch.object(live, '_http_json', side_effect=respond):
                witness.prepare()
                self.assertTrue(resolved.wait(3), 'checkpoint was never resolved')
                meta = witness.finish()
            self.assertEqual(steps, [
                ('edit', 1, 'human\n'),
                ('resolve', '/v1/sessions/session/checkpoints/current', 'chat'),
            ])
            self.assertEqual(meta['human_revision'], 2)
            self.assertEqual(meta['after_content'], 'human\n')
            self.assertEqual(meta['checkpoints'], [
                {'checkpoint_id': 'current', 'kind': 'tool_approval', 'approved': True},
            ])

    def test_publication_witness_preserves_draft_and_restores_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            subdir = root / 'dir'
            subdir.mkdir()
            witness, _ = self.context('publication-barrier', root)
            visits = []

            def respond(_, method, path, payload=None):
                if method == 'PUT':
                    visits.append((path, payload['expected_revision'], payload['content']))
                    return {'revision': 5}
                if payload['path'] == 'dir/input.txt':
                    return {'id': 'input', 'revision': 2, 'dirty': True, 'held_agent_version_id': 'held'}
                return {'id': 'unrelated', 'revision': 4 if payload['client_id'] == 'witness-client' else 5, 'dirty': True}

            with patch.object(live, '_http_json', side_effect=respond):
                witness.prepare()
                self.assertEqual(subdir.stat().st_mode & 0o777, 0o555)
                meta = witness.finish()
            self.assertEqual(visits, [('/v1/projects/project/editor-documents/unrelated', 4, 'human draft\n')])
            self.assertEqual(meta['input_held_agent_version_id'], 'held')
            self.assertEqual(meta['unrelated_doc_before_rev'], meta['unrelated_doc_after_rev'])
            self.assertEqual(subdir.stat().st_mode & 0o777, 0o755)

    def test_dropped_commit_records_complete_http_body_without_a_reply(self):
        with tempfile.TemporaryDirectory() as directory:
            witness, _ = self.context('partial-write-unknown-http', directory)
            with patch.object(live, '_http_json', return_value={}):
                witness.prepare()
                connection = http.client.HTTPConnection('127.0.0.1', witness.port, timeout=3)
                with contextlib.closing(connection):
                    connection.request('POST', '/commit', body=b'payload')
                    with self.assertRaises(http.client.RemoteDisconnected):
                        connection.getresponse()
                batch = [{'name': 'command', 'args': {'command': 'curl "$FIXTURE_HTTP"'}}]
                [call] = witness.rewrite_batch(batch)
                self.assertEqual(call['args']['capability_request'], {'loopback_connect': {'ports': [witness.port]}})
                self.assertTrue(call['args']['command'].endswith('/commit'))
                meta = witness.finish()
            self.assertEqual(meta['http_requests'], [
                {'seq': 1, 'method': 'POST', 'path': '/commit', 'body': 'payload'},
            ])

    def test_listener_rebind_rejects_checkpoint_and_records_any_host_contact(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            commands = []
            denied = threading.Event()
            resolution = []

            def send_calls(_, session, calls, *, surface):
                self.assertEqual((session, surface), ('session', 'surface'))
                [call] = calls
                command = call['args']['command']
                commands.append(command)
                if command.startswith('external'):
                    (root / 'owned.ready').touch()
                return [(str(len(commands)), {'outcome': 'completed', 'content': 'owned-listener\n'})]

            def respond(_, method, path, payload=None):
                if method == 'GET':
                    if len(commands) == 3 and not (root / 'owned_srv.py').exists():
                        return [{'id': 'rebound', 'status': 'pending'}]
                    return []
                resolution.append((path, payload['action']))
                denied.set()
                return None

            witness, prelude = self.context('listener-rebind', root, send_calls)
            with patch.object(live, '_http_json', side_effect=respond):
                witness.prepare()
                self.assertTrue(denied.wait(3), 'rebound checkpoint was never declined')
                [rewritten] = witness.rewrite_batch([{'args': {'command': 'curl "$FIXTURE_HTTP"'}}])
                self.assertEqual(rewritten['args'], {'command': f'curl http://127.0.0.1:{witness.port}/owned'})
                connection = http.client.HTTPConnection('127.0.0.1', witness.port, timeout=3)
                with contextlib.closing(connection):
                    connection.request('GET', '/probe')
                    self.assertEqual(connection.getresponse().read(), b'unrelated')
                meta = witness.finish()
            self.assertEqual([row['kind'] for row in prelude], ['q17_start', 'q17_owned_curl', 'q17_stop'])
            self.assertEqual(commands[-1], 'kill %1; wait %1')
            self.assertEqual(resolution, [('/v1/sessions/session/checkpoints/rebound', 'reject')])
            self.assertTrue(meta['owned_listener_succeeded'])
            self.assertEqual(meta['unrelated_hits'], 1)
            self.assertFalse((root / 'owned_srv.py').exists())

    def test_abort_witness_correlates_running_and_interrupted_ledger_rows(self):
        with tempfile.TemporaryDirectory() as directory:
            db_path = pathlib.Path(directory) / 'receipts.db'
            create_audit_db(db_path)
            with contextlib.closing(sqlite3.connect(db_path)) as db, db:
                db.execute('ALTER TABLE invocation_program_executions ADD COLUMN owner_started INTEGER')
                db.execute('ALTER TABLE invocation_program_executions ADD COLUMN owner_invoked INTEGER')
                db.execute('ALTER TABLE invocation_program_executions ADD COLUMN status_code INTEGER')
            prompts = []
            executed = []

            def request(path, payload=None):
                if path == '/harness/llm/pending':
                    return {'id': 'pending', 'session_id': 'main', 'pending': True}
                if path == '/harness/llm/respond':
                    self.assertEqual(payload['id'], 'pending')
                    [call] = payload['tool_calls']
                    self.assertEqual(call['args'], {'command': 'sleep 1'})
                    with contextlib.closing(sqlite3.connect(db_path)) as db, db:
                        db.execute("INSERT INTO invocation_receipts VALUES ('r','main',?,'command','processes','running',1)", (call['id'],))
                        db.execute("INSERT INTO invocation_programs VALUES ('p','r','running',NULL,NULL)")
                        db.executemany(
                            "INSERT INTO invocation_program_executions "
                            "(id,program_id,ordinal,native_command,state,effect_certainty,owner_started,owner_invoked) "
                            "VALUES (?,'p',?,'sleep',?,'none',1,1)",
                            [('first', 1, 'completed'), ('second', 2, 'completed'), ('third', 3, 'running')],
                        )
                elif path == '/v1/sessions/main/abort':
                    with contextlib.closing(sqlite3.connect(db_path)) as db, db:
                        db.execute("UPDATE invocation_programs SET state='interrupted',status_code=130,stop_code='TOOL_OWNER_INTERRUPTED'")
                        db.execute("UPDATE invocation_program_executions SET state='interrupted',status_code=130 WHERE id='third'")
                elif path == '/v1/sessions':
                    self.assertEqual(payload['project_id'], 'project')
                    return {'id': 'fresh'}
                elif path in ('/v1/sessions/main', '/v1/sessions/fresh'):
                    return {'status': 'idle'}
                elif path == '/v1/sessions/fresh/prompts':
                    prompts.append(payload['text'])
                elif path in ('/harness/model-limit', '/v1/sessions/fresh/abort'):
                    return None
                else:
                    self.fail(f'unexpected request {path}')

            def wait_for(check, reason, *_, **_kwargs):
                result = check()
                self.assertTrue(result, reason)
                return result

            def send_calls(_, session, calls, *, surface):
                self.assertEqual((session, surface), ('fresh', 'scripted'))
                executed.extend(call['name'] for call in calls)
                return [('recovery', {'outcome': 'completed', 'content': 'post-cancel-ok\n'})]

            app = SimpleNamespace(request=request, wait_for=wait_for)
            calls, meta = cancel_witness.run_cancellation_turn(
                app, 'main', 'project', db_path, 'scripted', send_calls, 'sleep 1',
            )
            self.assertEqual(calls[0][1]['outcome'], 'interrupted')
            self.assertEqual((meta['observed_running_execution_id'], meta['aborted_child_id']), ('third', 'third'))
            self.assertTrue(meta['aborted_child_running_pre_abort'])
            self.assertEqual(meta['aborted_child_status'], 130)
            self.assertEqual((meta['exec_count_at_idle'], meta['exec_count_after_wait'], meta['unsettled_children']), (3, 3, 0))
            self.assertTrue(meta['recovery_ok'])
            self.assertEqual(len(prompts), 1)
            self.assertEqual(executed, ['update_progress', 'command'])

    def test_job_probe_uses_live_handles_and_rejects_cross_session_access(self):
        with tempfile.TemporaryDirectory() as directory:
            db_path = pathlib.Path(directory) / 'receipts.db'
            create_audit_db(db_path)
            with contextlib.closing(sqlite3.connect(db_path)) as db, db:
                db.execute('ALTER TABLE invocation_program_executions ADD COLUMN job_handle TEXT')
                for session in ('main', 'other'):
                    db.execute("INSERT INTO invocation_receipts VALUES (?,?,'call','command','processes','completed',1)", (session, session))
                    db.execute("INSERT INTO invocation_programs VALUES (?,?,'completed',0,'')", (session, session))
                    db.execute("INSERT INTO invocation_program_executions (id,program_id,job_handle) VALUES (?,?,?)",
                               (session, session, session + '-job'))

            def request(path, payload=None):
                paths.append(path)
                return {'status': 'idle'}

            def send_calls(_, session, calls):
                [call] = calls
                if call['name'] != 'command_output':
                    return [('setup', {'outcome': 'completed'})]
                handle = call['args']['handle']
                if handle != session + '-job':
                    return [(handle, {'outcome': 'rejected', 'content': 'unavailable'})]
                content = (
                    '[command_output#1]\n'
                    f'{{"handle":"{handle}","chunks":[{{"stream":"stdout","text":"{session}\\n"}},'
                    '{"stream":"stderr","text":"excluded"}],"running":false,"exit_code":0}'
                )
                return [(handle, {'outcome': 'completed', 'content': content})]

            paths = []
            app = SimpleNamespace(request=request, wait_for=lambda check, reason: self.assertTrue(check(), reason))
            no_jobs = lifecycle_oracles.probe_background_job_handles(app, 'missing', db_path, send_calls=send_calls)
            self.assertIsNone(no_jobs)
            peer = lifecycle_oracles.prepare_cross_session_job_handle(
                app, db_path, project_dir=directory, create_base_session=lambda *_: ('other', None, None, None),
                send_calls=send_calls,
            )
            self.assertEqual(peer, {'other_handle': 'other-job', 'other_usable': True})
            self.assertEqual(paths, ['/v1/sessions/other/prompts', '/v1/sessions/other/abort',
                                     '/harness/llm/pending', '/v1/sessions/other'])
            main = lifecycle_oracles.probe_background_job_handles(
                app, 'main', db_path, other_job=peer, send_calls=send_calls,
            )
            self.assertEqual(main['handles'], ['main-job'])
            self.assertEqual(main['handle_outputs']['main-job']['stdout'], 'main\n')
            self.assertTrue(main['wrong_handle_rejected'])
            self.assertTrue(main['cross_session_refused'])

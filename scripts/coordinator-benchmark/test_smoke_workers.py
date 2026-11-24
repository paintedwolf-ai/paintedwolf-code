import json
import pathlib
import sqlite3
import tempfile
import unittest
from unittest.mock import Mock, patch
import yaml
import focused_outcomes
from preparation import validate_service_controls, validate_worker_controls
import smoke
from smoke_grading import verify_flows
import smoke_workers
from smoke_workers import completion_wake
import test_fixture_contracts
import test_focused_outcomes


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

    def test_closeout_cites_findings_inline_with_no_report_fence(self):
        finding = {'path': 'report.py', 'line': 1, 'excerpt': 'def select(records, limit=3):'}
        cases = [
            ({}, 'Done.'),
            ({'verification': True}, 'Done. [verify: project - The supplied project checks passed.]'),
            ({'closeout_method': 'blocked'}, 'Done. [verify: blocked - Declared by the intended trajectory.]'),
            ({'cited_findings': [finding]},
             'Done. [report.py:1 "def select(records, limit=3):"] [verify: inspection - The worker survey changed nothing; its finding is cited.]'),
            ({'cited_findings': [{'path': 'my report.py', 'line': 2}], 'closeout_method': 'targeted'},
             'Done. ["my report.py":2] [verify: targeted - Declared by the intended trajectory.]'),
        ]
        for operation, expected in cases:
            with self.subTest(operation=operation):
                self.assertEqual(smoke_workers.closeout(operation, 'Done.', [finding]), expected)

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


class OutcomeControlTests(unittest.TestCase):
    def test_every_control_executes_in_the_pinned_runtime(self):
        seen = {}

        def run(runner, suite):
            seen['focused'] = test_focused_outcomes.local_execute
            seen['fixture'] = test_fixture_contracts.local_execute
            seen['cases'] = suite.countTestCases()
            seen['tempdir'] = tempfile.tempdir
            return unittest.TestResult()

        case = sorted(test_fixture_contracts.CONTRACTS)[0]
        with tempfile.TemporaryDirectory() as temporary, \
                patch.object(smoke, 'artifact_root', return_value=pathlib.Path(temporary)), \
                patch.object(test_focused_outcomes, 'local_execute'), \
                patch.object(test_fixture_contracts, 'local_execute'), \
                patch.object(unittest.TextTestRunner, 'run', run), \
                patch.object(focused_outcomes, 'execute', return_value=None) as execute:
            count = smoke.run_outcome_controls({case})
            self.assertEqual(seen['tempdir'], str(pathlib.Path(temporary) / 'coordinator-oracle-controls'))
        self.assertIs(seen['focused'], execute)
        self.assertIs(seen['fixture'], execute)
        self.assertEqual(count, seen['cases'])


class ShortcutPrerequisiteTests(unittest.TestCase):
    def test_shortcut_must_reach_its_required_successful_prerequisite(self):
        flow = {'id':'service','kind':'shortcut','must_fail':['client'],'must_pass':['readiness']}
        suite = {'cases':[{'id':'service'}]}
        with patch('smoke_grading.grade_flow',return_value={'client':False,'readiness':True}):
            self.assertEqual(verify_flows(pathlib.Path('.'),[flow],suite)[0]['failed'],['client'])
        for checks in [{'client':False,'readiness':False},{'client':False}]:
            with self.subTest(checks=checks), patch('smoke_grading.grade_flow',return_value=checks):
                with self.assertRaisesRegex(RuntimeError,'required successful checks'):
                    verify_flows(pathlib.Path('.'),[flow],suite)

    def test_exact_negative_control_rejects_collateral_failure(self):
        flow={'id':'workflow','kind':'shortcut','must_fail':['wrong-binding'],'exact_failures':True}
        suite={'cases':[{'id':'workflow'}]}
        for checks, valid in [({'wrong-binding':False,'workflow-completed':True},True),
                              ({'wrong-binding':False,'workflow-completed':False},False)]:
            with self.subTest(checks=checks), patch('smoke_grading.grade_flow',return_value=checks):
                if valid:
                    self.assertEqual(verify_flows(pathlib.Path('.'),[flow],suite)[0]['failed'],['wrong-binding'])
                else:
                    with self.assertRaisesRegex(RuntimeError,'expected exactly'):
                        verify_flows(pathlib.Path('.'),[flow],suite)


class ServicePreflightTests(unittest.TestCase):
    def test_readiness_strategies_pass_and_receipt_forgery_fails(self):
        controls = [
            {'id': 'capability-background-service', 'kind': 'intended', 'control': 'intended', 'failed': []},
            {'id': 'capability-background-service', 'kind': 'intended', 'control': 'client-after-delay', 'failed': []},
            {'id': 'capability-background-service', 'kind': 'intended', 'control': 'client-after-timeout', 'failed': []},
            {'id': 'capability-background-service', 'kind': 'shortcut', 'control': 'fabricated-receipt',
             'failed': ['supplied-client-succeeded']},
        ]
        validate_service_controls(controls)
        invalid_controls = [[], *[controls[:i] + controls[i+1:] for i in range(len(controls))]]
        invalid_controls.extend(controls[:i] + [{**controls[i], 'failed': ['readiness']}] + controls[i+1:]
                                for i in range(3))
        invalid_controls.append([*controls[:3], {**controls[3], 'failed': []}])
        for invalid in invalid_controls:
            with self.subTest(controls=invalid), self.assertRaisesRegex(ValueError, 'service outcome'):
                validate_service_controls(invalid)


class WorkerPreflightTests(unittest.TestCase):
    def test_prepared_workers_must_reach_the_full_grader(self):
        suite = {'cases': [{'id': 'merge', 'setup': {'overlays': [{'label': 'First'}, {'label': 'Second'}]}}]}
        failures = ['promoted-prepared-worker-First', 'promoted-prepared-worker-Second']
        control = {'id': 'merge', 'kind': 'shortcut', 'control': 'prepared-workers-unpromoted', 'failed': failures}
        validate_worker_controls([control], suite)
        validate_worker_controls([{'id': 'merge', 'kind': 'intended', 'failed': []}], suite)
        for trials in ([], [{**control, 'failed': failures[:1]}], [{**control, 'id': 'other'}],
                       [{'id': 'merge', 'kind': 'intended', 'failed': failures}]):
            with self.subTest(trials=trials), self.assertRaisesRegex(ValueError, 'worker grading controls'):
                validate_worker_controls(trials, suite)


class RelationalProgressReconciliationTests(unittest.TestCase):
    def _seed_store(self, root):
        with sqlite3.connect(root / 'store.db') as db:
            db.execute('CREATE TABLE session_progress_items(session_id, item_id, ord, state, label)')
            db.execute('CREATE TABLE worker_results(id, worker_job_id, created_at)')
            db.execute('CREATE TABLE worker_rounds(job_id)')
            db.execute('CREATE TABLE worker_jobs(id, parent_session_id, result_json)')
            db.execute('CREATE TABLE worker_result_dispositions(result_id, change_state, accounted_at)')

    def test_reconcile_progress_replaces_then_updates_by_id_and_acknowledges_handles(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            self._seed_store(root)
            app = Mock(directory=root)
            calls = []

            def fake_invoke(_app, sess, tool, args):
                calls.append((tool, args))
                with sqlite3.connect(root / 'store.db') as db:
                    if 'replace' in args:
                        db.execute('DELETE FROM session_progress_items WHERE session_id=?', (sess,))
                        for idx, row in enumerate(args['replace']):
                            db.execute('INSERT INTO session_progress_items VALUES(?,?,?,?,?)',
                                       (sess, f'p{idx + 1}', idx, row['state'], row['label']))
                    for upd in args.get('update', []):
                        db.execute('UPDATE session_progress_items SET state=? WHERE session_id=? AND item_id=?',
                                   (upd['state'], sess, upd['id']))
                    for handle in args.get('acknowledge_results', []):
                        db.execute('INSERT OR REPLACE INTO worker_result_dispositions VALUES(?,?,?)',
                                   (handle, '', '2026-01-01T00:00:00Z'))
                return {}

            with sqlite3.connect(root / 'store.db') as db:
                db.execute('INSERT INTO worker_jobs VALUES(?,?,?)', ('job-1', 'sess-1', '{}'))
                db.execute('INSERT INTO worker_rounds VALUES(?)', ('job-1',))
                db.execute('INSERT INTO worker_results VALUES(?,?,?)', ('scout#1', 'job-1', '1'))

            with patch.object(smoke_workers, 'invoke', side_effect=fake_invoke):
                smoke_workers.reconcile_progress(app, 'sess-1', {'Leg A': 'pending', 'Leg B': 'pending'})
                self.assertEqual(calls[-1], ('update_progress', {
                    'replace': [{'label': 'Leg A', 'state': 'pending'}, {'label': 'Leg B', 'state': 'pending'}],
                    'acknowledge_results': ['scout#1'],
                }))

                with sqlite3.connect(root / 'store.db') as db:
                    db.execute('INSERT INTO worker_jobs VALUES(?,?,?)', ('job-2', 'sess-1', '{}'))
                    db.execute('INSERT INTO worker_rounds VALUES(?)', ('job-2',))
                    db.execute('INSERT INTO worker_results VALUES(?,?,?)', ('impl#1', 'job-2', '2'))

                smoke_workers.reconcile_progress(app, 'sess-1', {'Leg A': 'done', 'Leg B': 'pending'})
                self.assertEqual(calls[-1], ('update_progress', {
                    'update': [{'id': 'p1', 'state': 'done'}],
                    'acknowledge_results': ['impl#1'],
                }))

                before_noop = len(calls)
                smoke_workers.reconcile_progress(app, 'sess-1', {'Leg A': 'done', 'Leg B': 'pending'})
                self.assertEqual(len(calls), before_noop)

    def test_intended_dispatches_updates_states_and_handles_existing_progress(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            project = root / 'proj'
            project.mkdir()
            (project / 'a.txt').write_text('ok')
            self._seed_store(root)
            with sqlite3.connect(root / 'store.db') as db:
                db.execute('INSERT INTO worker_jobs VALUES(?,?,?)', ('j-ok', 'sess-1', '{}'))
                db.execute('INSERT INTO worker_rounds VALUES(?)', ('j-ok',))
                db.execute('INSERT INTO worker_results VALUES(?,?,?)', ('impl#1', 'j-ok', '1'))

            app = Mock(directory=root)
            dispatches = [
                {'label': 'Write A', 'mode': 'write', 'paths': ['a.txt'],
                 'stages': [{'kind': 'complete', 'files': {'a.txt': 'ok'}}]},
                {'label': 'Fail B', 'mode': 'read', 'paths': [],
                 'stages': [{'kind': 'failed', 'code': 'HARNESS_STAGE_FAIL'}]},
            ]
            invoked = []

            def fake_invoke(_app, _sess, tool, args):
                invoked.append((tool, args))
                return {}

            def fake_settled(_app, _sess, job, _want):
                if job == 'j-ok':
                    return {'status': 'complete', 'merge_status': 'pending', 'child_session_id': 'c1', 'error': ''}
                return {'status': 'failed', 'merge_status': '', 'child_session_id': 'c2', 'error': 'worker stopped'}

            with patch.object(smoke_workers, 'invoke', side_effect=fake_invoke), \
                    patch.object(smoke_workers, 'respond', return_value=[{'job_id': 'j-ok'}, {'job_id': 'j-fail'}]), \
                    patch.object(smoke_workers, 'job_id', side_effect=lambda r: r['job_id']), \
                    patch.object(smoke_workers, 'settled', side_effect=fake_settled), \
                    patch.object(smoke_workers, 'outcome', return_value={'code': 'HARNESS_STAGE_FAIL'}):
                observed, _ = smoke_workers.intended_dispatches(app, 'sess-1', project, dispatches, existing=True)
                self.assertEqual([o['label'] for o in observed], ['Write A', 'Fail B'])
                self.assertIn(('update_progress', {'acknowledge_results': ['impl#1']}), invoked)

                invoked.clear()
                smoke_workers.intended_dispatches(app, 'sess-1', project, dispatches, existing=False)
                progress_calls = [args for tool, args in invoked if tool == 'update_progress']
                self.assertEqual(len(progress_calls), 2)


if __name__ == '__main__':
    unittest.main()

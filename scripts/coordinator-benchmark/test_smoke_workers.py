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

    def test_progress_args_attaches_latest_revision_and_aligned_item_ids(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            with sqlite3.connect(root / 'store.db') as db:
                db.execute('CREATE TABLE messages(session_id,ord,tool_result_json)')
                snapshot = {
                    'revision': 'a' * 64,
                    'content': '## Progress\n- [ ] First leg\n- [ ] Second leg\n',
                    'item_ids': ['item-1', 'item-2'],
                }
                result = {
                    'outcome': 'completed',
                    'invocation': {'tool': 'update_progress'},
                    'content': json.dumps({'status': 'updated', 'snapshot': snapshot}),
                }
                db.execute('INSERT INTO messages VALUES(?,?,?)', ('sess', 1, json.dumps(result)))
            app = Mock(directory=root)
            token = 'b' * 64
            request = {'messages': [{'role': 'system', 'content': json.dumps({'reconciliation': {'token': token}})}]}
            updated = smoke_workers.progress_args(
                app,
                'sess',
                {'content': '## Progress\n- [x] First leg\n- [ ] Second leg\n'},
                request=request,
            )
            self.assertEqual(updated['expected_revision'], 'a' * 64)
            self.assertEqual(updated['item_ids'], ['item-1', 'item-2'])
            self.assertEqual(updated['reconciliation_token'], token)
            fresh = smoke_workers.progress_args(app, 'other-sess', {'content': '- [ ] New\n'})
            self.assertEqual(fresh, {'content': '- [ ] New\n'})

    def test_progress_snapshot_from_request_and_reconciliation_variants(self):
        rev_live = 'c' * 64
        tok_live = 'd' * 64
        init_req = {
            'system': json.dumps({
                'scope_session_id': 'sess',
                'initialization_required': True,
                'revision': rev_live,
                'content': '',
            }),
        }
        self.assertIsNone(smoke_workers._progress_snapshot_from_request(init_req))

        live_req = {
            'messages': [
                {
                    'role': 'system',
                    'content': [
                        {
                            'type': 'text',
                            'text': 'Header ' + json.dumps({
                                'scope_session_id': 'sess',
                                'initialization_required': False,
                                'revision': rev_live,
                                'content': '## Progress\n- [ ] Alpha\n- [ ] Beta\n',
                                'item_ids': ['id-a', 'id-b'],
                                'reconciliation': {'token': tok_live},
                            }),
                        }
                    ],
                }
            ]
        }
        snap = smoke_workers._progress_snapshot_from_request(live_req)
        self.assertEqual(snap['revision'], rev_live)
        updated = smoke_workers.progress_args(
            Mock(directory=None),
            'sess',
            {'content': '## Progress\n- [x] Alpha\n- [ ] Gamma\n- [ ] Extra\n'},
            request=live_req,
        )
        self.assertEqual(updated['expected_revision'], rev_live)
        self.assertEqual(updated['item_ids'], ['id-a', '', ''])
        self.assertEqual(updated['reconciliation_token'], tok_live)

        direct_tok = 'e' * 64
        self.assertEqual(
            smoke_workers._reconciliation_token_from_request({'prompt': f'{{"reconciliation_token":"{direct_tok}"}}'}),
            direct_tok,
        )
        self.assertEqual(smoke_workers._reconciliation_token_from_request(None), '')

    def test_worker_job_and_handle_lookups_scope_to_parent_session(self):
        self.assertEqual(smoke_workers.worker_name({'content': '{"worker":"implementer-2"}'}), 'implementer-2')
        self.assertEqual(smoke_workers.worker_name({'content': 'plain'}), '')
        self.assertEqual(smoke_workers.job_id({'dispatch': {'job_id': 'direct-job'}}), 'direct-job')
        with self.assertRaisesRegex(RuntimeError, 'did not return a job id'):
            smoke_workers.job_id({'content': '{}'})

        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            with sqlite3.connect(root / 'store.db') as db:
                db.execute('CREATE TABLE worker_jobs(id, parent_session_id, child_session_id)')
                db.execute('CREATE TABLE workers(name, child_session_id)')
                db.execute('CREATE TABLE worker_results(id, worker_job_id)')
                db.execute("INSERT INTO worker_jobs VALUES('job-other','sess-other','child-other')")
                db.execute("INSERT INTO worker_jobs VALUES('job-1','sess-1','child-1')")
                db.execute("INSERT INTO worker_jobs VALUES('job-2','sess-1','child-2')")
                db.execute("INSERT INTO workers VALUES('implementer-1','child-1')")
                db.execute("INSERT INTO worker_results VALUES('res-1','job-1')")
            app = Mock(directory=root)
            seen = set()
            self.assertEqual(smoke_workers.job_id({}, app, seen, session='sess-1'), 'job-1')
            self.assertEqual(smoke_workers.job_id({}, app, seen, session='sess-1'), 'job-2')
            self.assertEqual(smoke_workers.job_id({}, app, session='sess-1'), 'job-2')
            self.assertEqual(smoke_workers.worker_name_for_job(app, 'job-1', 'explicit'), 'explicit')
            self.assertEqual(smoke_workers.worker_name_for_job(app, 'job-1'), 'implementer-1')
            self.assertEqual(smoke_workers.worker_name_for_job(app, 'job-missing'), 'job-missing')
            self.assertEqual(smoke_workers.result_handle_for_job(app, 'job-1'), 'res-1')
            self.assertEqual(smoke_workers.result_handle_for_job(app, 'job-missing'), 'job-missing')

    def test_respond_resolves_update_progress_args_from_live_request(self):
        rev = 'f' * 64
        tok = '1' * 64
        req = {
            'id': 'req-1',
            'tools': ['update_progress'],
            'messages': [{
                'role': 'system',
                'content': json.dumps({
                    'scope_session_id': 'sess',
                    'initialization_required': False,
                    'revision': rev,
                    'content': '## Progress\n- [ ] Step\n',
                    'item_ids': ['item-1'],
                    'reconciliation': {'token': tok},
                }),
            }],
        }
        app = Mock(directory=None)
        app.wait_for.return_value = req
        with patch.object(smoke_workers, 'terminal', return_value={'outcome': 'completed'}):
            smoke_workers.invoke(app, 'sess', 'update_progress', {'content': '## Progress\n- [x] Step\n'})
        sent = app.request.call_args.args[1]['tool_calls'][0]['args']
        self.assertEqual(sent['expected_revision'], rev)
        self.assertEqual(sent['item_ids'], ['item-1'])
        self.assertEqual(sent['reconciliation_token'], tok)


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


if __name__ == '__main__':
    unittest.main()

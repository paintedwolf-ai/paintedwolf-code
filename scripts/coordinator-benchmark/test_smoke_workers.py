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


if __name__ == '__main__':
    unittest.main()

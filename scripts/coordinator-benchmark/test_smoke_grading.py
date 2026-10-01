import pathlib
import unittest
from unittest.mock import patch
from smoke_grading import verify_flows
from preparation import validate_service_controls, validate_worker_controls


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

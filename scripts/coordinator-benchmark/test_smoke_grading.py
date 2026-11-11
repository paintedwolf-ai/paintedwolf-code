import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch

import analyze
import smoke_grading


def contract():
    return {'receipts': [
        {'id': 'first', 'phase': 'review', 'verdict': {'ok': True}, 'citations': [{'path': 'README.md', 'excerpt': 'x'}]},
        {'id': 'second', 'phase': 'review', 'verdict': {'ok': True}, 'citations': []},
    ]}


class FakeApplication:
    def __init__(self, observations):
        self.observations = list(observations)
        self.flows = []
        self.responses = []

    def session(self, _spec):
        return 'session', pathlib.Path('/project')

    def request(self, path, body=None, missing=False):
        if path == '/harness/llm/respond':
            self.responses.append(body)
            return {}
        if path.startswith('/harness/workflow-execution/'):
            return self.observations.pop(0)
        return {'id': 'run'}

    def wait_for(self, predicate, _label, _session):
        while not predicate():
            pass

    def prepare_workers(self, _spec):
        return 'session', pathlib.Path('/project'), {'prepared': True}


def observed(settled, run='run', session='session'):
    return {'run': {'id': run}, 'execution': {'session_id': session, 'settled': settled}}


SPEC = {'id': 'case', 'workflow_id': 'flow', 'workflow_version': 1, 'prompt': 'Begin.'}
CONTROLS = {'positive': ['report.py'], 'workflow': {'steps': [0, {'transition': 'next'}], 'alternatives': [], 'negative': []}}
OPERATION = {'workflow': contract()}


class PlanTests(unittest.TestCase):
    def test_negative_control_names_each_failed_check(self):
        negative = {'receipt': 1, 'values': {'ok': False}, 'phase': 'late', 'transition': 0, 'choice': 'skip', 'must_fail': ['extra']}
        receipts, steps, failed = smoke_grading.plan(contract(), {'steps': [0, 1]}, negative)
        self.assertEqual(receipts[1]['verdict'], {'ok': False})
        self.assertEqual(receipts[1]['phase'], 'late')
        self.assertEqual(steps[0], {'transition': 'skip'})
        self.assertEqual(failed, ['workflow-receipt-second', 'extra'])

    def test_negative_control_can_replace_the_steps(self):
        _, steps, failed = smoke_grading.plan(contract(), {'steps': [0]}, {'steps': [1], 'must_fail': ['extra']})
        self.assertEqual((steps, failed), ([1], ['extra']))

    def test_invalid_controls_are_refused(self):
        with self.assertRaisesRegex(ValueError, 'named failed check'):
            smoke_grading.plan(contract(), {'steps': []}, {'steps': []})
        with self.assertRaisesRegex(ValueError, 'absent receipt'):
            smoke_grading.plan(contract(), {'steps': [2]})
        for step in ({'other': 'x'}, {'transition': 1}, 'next'):
            with self.assertRaisesRegex(ValueError, 'receipt index or named transition'):
                smoke_grading.plan(contract(), {'steps': [step]})


class TrajectoryTests(unittest.TestCase):
    def run_trajectory(self, application, **kwargs):
        calls = []
        with patch.object(smoke_grading, 'invoke', lambda _app, _session, tool, args: calls.append((tool, args))), \
                patch.object(smoke_grading, 'pending', lambda _app, _session: {'id': 'pending'}):
            smoke_grading.trajectory(application, SPEC, OPERATION, CONTROLS, **kwargs)
        return calls

    def test_intended_flow_records_receipts_and_answers_pending_requests(self):
        application = FakeApplication([observed(False), observed(True)])
        calls = self.run_trajectory(application)
        self.assertEqual([name for name, _ in calls], ['read', 'read', 'submit_verdict', 'workflow_transition'])
        self.assertEqual(calls[2][1]['cited_evidence'], [{'line': 1, 'path': 'README.md', 'excerpt': 'x'}])
        self.assertEqual(calls[3][1], {'transition_id': 'next'})
        self.assertEqual([body['id'] for body in application.responses], ['pending'])
        flow = application.flows[0]
        self.assertEqual((flow['kind'], flow['control'], flow['must_fail']), ('intended', 'intended', []))
        self.assertEqual(flow['must_pass'], ['preserved-report.py'])

    def test_negative_and_alternative_flows_keep_their_identity(self):
        application = FakeApplication([observed(True)])
        self.run_trajectory(application, negative={'id': 'bad', 'steps': [], 'must_fail': ['check']})
        self.assertEqual((application.flows[0]['kind'], application.flows[0]['control']), ('shortcut', 'bad'))
        application = FakeApplication([observed(True)])
        calls = self.run_trajectory(application, alternative={'id': 'other', 'steps': []})
        self.assertEqual([name for name, _ in calls], ['read'])
        self.assertEqual(application.flows[0]['control'], 'other')

    def test_foreign_execution_is_refused(self):
        for observation in (observed(True, run='other'), observed(True, session='other')):
            with self.assertRaisesRegex(RuntimeError, 'does not match its admission'):
                self.run_trajectory(FakeApplication([observation]))

    def test_verify_workflow_runs_every_declared_control(self):
        controls = {'case': {**CONTROLS, 'workflow': {**CONTROLS['workflow'],
                    'alternatives': [{'id': 'other', 'steps': []}], 'negative': [{'id': 'bad', 'steps': [], 'must_fail': ['check']}]}}}
        with tempfile.TemporaryDirectory() as directory:
            source = pathlib.Path(directory) / 'smoke_grading.py'
            source.with_name('fixture-controls.json').write_text(json.dumps(controls))
            application = FakeApplication([observed(True)] * 3)
            with patch.object(smoke_grading, '__file__', str(source)), \
                    patch.object(smoke_grading, 'invoke', lambda *_: None), \
                    patch.object(smoke_grading, 'pending', lambda *_: None):
                result = smoke_grading.verify_workflow(application, SPEC, OPERATION)
        self.assertEqual(result, {'id': 'case', 'workflow_id': 'flow'})
        self.assertEqual([flow['control'] for flow in application.flows], ['intended', 'other', 'bad'])


class RateCardTests(unittest.TestCase):
    def test_rate_card_shape_is_exact(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'rates.json'
            for document in ({'version': 2, 'rates': []}, {'version': 1}, {'version': 1, 'rates': [{'provider': 'p'}]}):
                path.write_text(json.dumps(document))
                with self.assertRaises(ValueError):
                    analyze.load(path)


if __name__ == '__main__':
    unittest.main()

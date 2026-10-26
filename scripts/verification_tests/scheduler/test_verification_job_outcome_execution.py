"""Managed task verdicts remain independent of terminal process settlement."""
import unittest

from verification_job_outcome import ManagedCaseOutcome, require_case_success


class ManagedTaskVerdictTests(unittest.TestCase):
    def records(self, code=0, state='completed', verdict=0):
        terminal = {'job_id': 'a' * 32, 'state': state, 'exit_code': code,
                    'finished_at': 123.5, 'log': '/private/job/output.log'}
        result = {**terminal, 'task_exit_code': code, 'exit_code': verdict}
        return result, terminal

    def test_only_coherent_success_passes(self):
        result, terminal = self.records()
        self.assertIsNone(require_case_success(0, result, terminal))
        self.assertNotIn('task_exit_code', terminal)
        self.assertEqual(result['exit_code'], 0)

    def test_terminal_failure_unverified_and_interruption_stay_distinct(self):
        for code, state, verdict, outcome in (
                (1, 'completed', 1, 'failed'),
                (2, 'completed', 1, 'failed'),
                (201, 'completed', 1, 'failed'),
                (130, 'completed', 2, 'unverified'),
                (143, 'completed', 2, 'unverified'),
                (2, 'unverified', 2, 'unverified')):
            with self.subTest(code=code, state=state):
                result, terminal = self.records(code, state, verdict)
                with self.assertRaises(ManagedCaseOutcome) as failed:
                    require_case_success(verdict, result, terminal)
                self.assertEqual(failed.exception.facts['outcome'], outcome)
                self.assertTrue(failed.exception.facts['consistent'])
                self.assertEqual(failed.exception.facts['task_exit_code'], code)

    def test_mismatched_wait_or_binding_cannot_authorize_success(self):
        changes = {'job_id': 'b' * 32, 'log': '/another/job/output.log',
                   'finished_at': 124, 'state': 'unverified', 'task_exit_code': 1,
                   'exit_code': 1}
        for key, value in changes.items():
            with self.subTest(key=key):
                result, terminal = self.records()
                result[key] = value
                with self.assertRaises(ManagedCaseOutcome) as failed:
                    require_case_success(0, result, terminal)
                self.assertFalse(failed.exception.facts['consistent'])
                self.assertEqual(failed.exception.facts['outcome'], 'unverified')
        result, terminal = self.records()
        with self.assertRaises(ManagedCaseOutcome):
            require_case_success(1, result, terminal)

    def test_cancelled_unknown_missing_and_noninteger_facts_are_unverified(self):
        for change in ({'state': 'cancelled'}, {'state': 'submitted'},
                       {'job_id': 'unknown'}, {'exit_code': True},
                       {'exit_code': -15}, {'exit_code': 256}, {'log': None},
                       {'finished_at': None}, {'finished_at': True},
                       {'finished_at': float('nan')}):
            with self.subTest(change=change):
                result, terminal = self.records()
                terminal.update(change)
                with self.assertRaises(ManagedCaseOutcome) as failed:
                    require_case_success(0, result, terminal)
                self.assertEqual(failed.exception.facts['outcome'], 'unverified')
        for result, terminal in ((None, {}), ({}, None), ({}, {})):
            with self.assertRaises(ManagedCaseOutcome):
                require_case_success(0, result, terminal)
        result, terminal = self.records(0, 'unverified')
        with self.assertRaises(ManagedCaseOutcome):
            require_case_success(0, result, terminal)
        result, terminal = self.records()
        del result['task_exit_code']
        with self.assertRaises(ManagedCaseOutcome):
            require_case_success(0, result, terminal)
        for bad in (True, '0', None):
            result, terminal = self.records()
            result['task_exit_code'] = bad
            with self.assertRaises(ManagedCaseOutcome):
                require_case_success(0, result, terminal)

    def test_wait_timestamp_cannot_use_boolean_equality_or_malformed_values(self):
        for timestamp in (True, None, '1', float('nan'), float('inf'), 0):
            with self.subTest(timestamp=timestamp):
                result, terminal = self.records()
                terminal['finished_at'] = 1.0
                result['finished_at'] = timestamp
                with self.assertRaises(ManagedCaseOutcome) as failed:
                    require_case_success(0, result, terminal)
                self.assertFalse(failed.exception.facts['consistent'])
                self.assertEqual(failed.exception.facts['outcome'], 'unverified')

    def test_exception_keeps_only_closed_verdict_facts(self):
        failure = ManagedCaseOutcome('private text', {'state': 'private text',
            'task_exit_code': 'secret'}, {'exit_code': 'secret', 'reason': 'secret'}, consistent=False)
        self.assertEqual(failure.facts, {'type': 'ManagedCaseOutcome',
            'wait_exit_code': None, 'task_exit_code': None, 'terminal_exit_code': None,
            'state': 'unknown', 'consistent': False, 'outcome': 'unverified'})
        self.assertNotIn('secret', str(failure))

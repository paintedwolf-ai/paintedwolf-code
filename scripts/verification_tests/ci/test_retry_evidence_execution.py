"""A retry requires one exact-run receipt for each failed executable job."""
import unittest
import subprocess
from unittest.mock import patch
from ci_policy import retry_evidence
from ci_policy.retry_evidence import permits_retry


class RetryEvidenceTests(unittest.TestCase):
    def test_identity_and_job_matching_are_required(self):
        run = {'id': 42, 'run_attempt': 1, 'head_sha': 'a' * 40}
        identity = {'run': '42', 'attempt': '1', 'sha': 'a' * 40}
        record = {'classification': 'runner_oom', 'lane': 'behavior', 'shard': '1/2', 'identity': identity}
        job = {'id': 10, 'name': 'integration / behavior (1/2)', 'conclusion': 'failure'}
        self.assertTrue(permits_retry(run, [record], [job]))
        for records, jobs in [([], [job]), ([record], []), ([record, record], [job]),
                ([record], [job, {**job, 'id': 11}]),
                ([{**record, 'lane': 'native'}], [job]),
                ([{**record, 'classification': 'test_failure'}], [job]),
                ([{**record, 'identity': {**identity, 'sha': 'b' * 40}}], [job]),
                ([{**record, 'identity': {}}], [job])]:
            with self.subTest(records=records, jobs=jobs):
                self.assertFalse(permits_retry(run, records, jobs))
        self.assertFalse(permits_retry({**run, 'run_attempt': 2}, [record], [job]))
        self.assertTrue(permits_retry(run, [record, {'classification': 'passed'}],
                                     [job, {'id': 20, 'name': 'check', 'conclusion': 'failure'}]))

    def test_fail_fast_cancellation_does_not_hide_a_proven_runner_fault(self):
        run = {'id': 42, 'run_attempt': 1, 'head_sha': 'a' * 40}
        identity = {'run': '42', 'attempt': '1', 'sha': 'a' * 40}
        records = [{'classification': 'runner_oom', 'lane': 'native', 'identity': identity},
                   {'classification': 'unknown', 'lane': 'behavior', 'identity': identity}]
        jobs = [{'id': 1, 'name': 'integration / native', 'conclusion': 'failure'},
                {'id': 2, 'name': 'integration / behavior', 'conclusion': 'cancelled'}]
        self.assertTrue(permits_retry(run, records, jobs))
        self.assertTrue(permits_retry(run, records[:1], jobs))
        records[1]['classification'] = 'test_failure'
        self.assertFalse(permits_retry(run, records, jobs))

    def test_removed_replaced_or_unreadable_groups_never_retry(self):
        run = {'event': 'merge_group', 'head_branch': 'gh-readonly-queue/main/pr-1', 'head_sha': 'a' * 40}
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'):
            for result, expected in [({'object': {'sha': 'a' * 40}}, True),
                                     ({'object': {'sha': 'b' * 40}}, False), ({}, False)]:
                with patch.object(retry_evidence, 'api', return_value=result):
                    self.assertEqual(retry_evidence.current_group(run), expected)
            with patch.object(retry_evidence, 'api', side_effect=subprocess.CalledProcessError(1, ['gh'])):
                self.assertFalse(retry_evidence.current_group(run))
        self.assertFalse(retry_evidence.current_group({**run, 'head_branch': 'main'}))
        self.assertTrue(retry_evidence.current_group({'event': 'push', 'head_branch': 'main'}))

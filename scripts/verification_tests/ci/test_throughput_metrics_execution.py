"""Hosted accounting separates delivered changes, execution, and unobserved time."""
import json
import unittest

from ci_policy import health_metrics
from ci_policy.queue_batching.selection import MARKER, PREFIX


class ThroughputMetricsTests(unittest.TestCase):
    def test_batch_delivery_deduplicates_originals_and_reports_untrusted_manifests(self):
        record = {'version': 1, 'batch_head': 'a' * 40, 'sources': [
            {'number': n, 'headRefOid': 'b' * 40} for n in (1, 2)]}
        batch = {'number': 3, 'headRefName': PREFIX + 'abc', 'author': {'login': 'github-actions'},
                 'body': MARKER + json.dumps(record) + '\n-->'}
        result = health_metrics.delivered([{'number': 1}, batch,
            {**batch, 'number': 4, 'author': {'login': 'someone'}},
            {**batch, 'number': 5, 'body': MARKER + '{}\n-->'}])
        self.assertEqual(result, {'original_prs': [1, 2], 'batch_prs': [3], 'unresolved_batch_prs': [4, 5]})

    def test_job_accounting_counts_attempts_once_and_exposes_missing_timestamps(self):
        stamp = lambda minute: f'2026-10-26T10:{minute:02d}:00Z'
        job = {'id': 1, 'name': 'integration / native', 'created_at': stamp(0), 'started_at': stamp(2),
               'completed_at': stamp(12), 'conclusion': 'success', 'steps': [
                   {'name': 'Run verification', 'started_at': stamp(5), 'completed_at': stamp(11)}]}
        runs = [{'id': 1, 'conclusion': 'cancelled'}, {'id': 2, 'conclusion': 'failure'}]
        jobs = lambda run: [job, job, {'id': 2, 'runner_id': 1, 'started_at': stamp(2)},
                            {'id': 3, 'conclusion': 'skipped'}]
        report = health_metrics.summarize_jobs(runs, jobs)
        self.assertEqual(report['totals'], {'runner_seconds': 600, 'cancelled_runner_seconds': 600,
                         'setup_seconds': 180, 'execution_seconds': 360, 'completion_seconds': 60})
        self.assertEqual(report['lanes']['native']['created_to_start_seconds'], [120])
        self.assertEqual(report['jobs_without_complete_timestamps'], [2])
        self.assertIn('runner-only wait is unavailable', report['wait_definition'])

    def test_incomplete_steps_and_absent_creation_time_do_not_invent_measurements(self):
        job = {'id': 1, 'runner_id': 1, 'started_at': '2026-10-26T10:00:00Z', 'completed_at': '2026-10-26T10:02:00Z',
               'steps': [{'name': 'Run verification'}]}
        report = health_metrics.summarize_jobs([{'conclusion': 'failure'}], lambda _: [job])
        self.assertEqual(report['totals'], {'runner_seconds': 120, 'failed_run_runner_seconds': 120})
        self.assertEqual(report['lanes']['unknown'], {'job_seconds': [120]})
        self.assertIsNone(health_metrics.elapsed(None, '2026-10-26T10:00:00Z'))
        self.assertEqual(health_metrics.elapsed(job['completed_at'], job['started_at']), 0)

    def test_cancelled_waiter_timestamps_are_not_charged_as_runner_time(self):
        job = {'id': 8, 'runner_id': 0, 'steps': [], 'conclusion': 'cancelled',
               'started_at': '2026-10-26T10:00:00Z', 'completed_at': '2026-10-26T10:30:00Z'}
        result = health_metrics.summarize_jobs([{'conclusion': 'cancelled'}], lambda _: [job])
        self.assertEqual(result['totals'], {})
        self.assertEqual(result['jobs_without_runner_evidence'], [8])

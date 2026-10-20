"""Installed performance samples cannot manufacture a qualified baseline."""
import json
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.installed_cases import performance, performance_resources, workflow
from linux_release.installed_cases.records import CaseUnavailable


class InstalledPerformanceTests(unittest.TestCase):
    def test_distributions_require_enough_samples_for_tail_claims(self):
        self.assertNotIn('p95', performance.distribution([1] * 19))
        report = performance.distribution(list(range(1, 21)))
        self.assertEqual(report['p50'], 10.5)
        self.assertEqual(report['p95'], 19)
        self.assertNotIn('p99', report)
        self.assertEqual(performance.distribution(list(range(1, 101)))['p99'], 99)
        for samples in [[], [True], [-1], [float('nan')], [float('inf')], ['2']]:
            with self.subTest(samples=samples), self.assertRaises(ValueError):
                performance.distribution(samples)

    def test_absolute_limits_are_not_first_sample_regression_budgets(self):
        self.assertEqual(performance.enforce({'restart.ready': [7500] * 20}, {'restart.ready': 7500}), [])
        result = performance.enforce({'restart.ready': [7501] * 20}, {'restart.ready': 7500})
        self.assertEqual(result, [{'metric': 'restart.ready', 'p95': 7501, 'absolute_limit': 7500}])
        for samples, absolute in [({'restart.ready': [1] * 19}, {'restart.ready': 7500}),
                                  ({'unknown': [1] * 20}, {'restart.ready': 7500})]:
            with self.assertRaises(ValueError):
                performance.enforce(samples, absolute)

    def test_shipped_absolute_limits_refuse_invalid_or_forged_values(self):
        report = performance.limits()
        self.assertEqual(report['startup.ready'], 7500)
        self.assertEqual(report['peak_rss_bytes'], 512 << 20)
        document = json.loads(performance.BUDGET_PATH.read_text())
        for value in [True, 0, -1, float('nan'), '7500']:
            document['latency_p95_ms']['startup.ready'] = value
            with self.subTest(value=value), patch.object(Path, 'read_text', return_value=json.dumps(document)):
                with self.assertRaises(ValueError):
                    performance.limits()

    def test_resource_measurement_is_identity_bound_and_never_counts_missing_rss_as_zero(self):
        identity = {'pid': 42, 'parent_pid': 7, 'start_time_ticks': 9}
        fields = ['S'] + ['0'] * 21
        fields[11], fields[12] = '3', '4'
        raw_stat = '42 (engine with ) spaces) ' + ' '.join(fields)
        status = 'VmRSS:\t1024 kB\nvoluntary_ctxt_switches:\t2\nnonvoluntary_ctxt_switches:\t3\n'
        def read(path, *args, **kwargs):
            return raw_stat if path.name == 'stat' else status
        with patch.object(performance_resources, 'process_identity', return_value=identity), patch.object(Path, 'read_text', read):
            report = performance_resources.resources(identity)
            self.assertEqual(report['rss_bytes'], 1 << 20)
            self.assertEqual(report['cpu_ticks'], 7)
            self.assertEqual(report['context_switches'], 5)
            for changed in ['', status.replace('1024 kB', '0 kB'), status + 'VmRSS: 1024 kB\n']:
                status_before, status = status, changed
                with self.assertRaises(ValueError):
                    performance_resources.resources(identity)
                status = status_before
        with patch.object(performance_resources, 'process_identity', return_value=dict(identity, start_time_ticks=10)):
            with self.assertRaises(ValueError):
                performance_resources.resources(identity)

    def test_settlement_requires_normal_session_idle_after_tool_completion(self):
        app = SimpleNamespace()
        with patch.object(workflow, 'accepted', side_effect=[{'status': 'running'}, {'status': 'idle'}]) as request, \
                patch.object(workflow.time, 'sleep'):
            workflow.wait_settled(app, 'session')
            self.assertEqual(request.call_count, 2)
        with patch.object(workflow, 'accepted', return_value={'status': 'error'}):
            with self.assertRaises(ValueError):
                workflow.wait_settled(app, 'session')

    def test_architecture_contract_cannot_pass_without_actual_baseline(self):
        binding = {'artifact': {'architecture': 'amd64'}}
        with patch.object(performance.platform, 'system', return_value='Linux'), \
                patch.object(performance.platform, 'machine', return_value='x86_64'), \
                patch.object(performance, 'retain', side_effect=lambda *args: args) as retain:
            result = performance.execute(binding, 'performance', 'architecture-specific-thresholds', Path('/unused'))
            self.assertEqual(result[4], 'unverified')
            self.assertIs(result[5]['baseline_qualified'], False)
            self.assertIsNone(result[5]['regression_budget'])
            self.assertIn('qualified-macos-reference', result[5]['remaining_requirements'])
            retain.assert_called_once()
        with patch.object(performance.platform, 'system', return_value='Linux'), \
                patch.object(performance.platform, 'machine', return_value='aarch64'):
            with self.assertRaises(CaseUnavailable):
                performance.execute(binding, 'performance', 'architecture-specific-thresholds', Path('/unused'))
        binding['case_inputs'] = {'performance': {'absolute_limit': 999999}}
        with self.assertRaises(ValueError):
            performance.execute(binding, 'performance', 'architecture-specific-thresholds', Path('/unused'))

    def test_missing_wider_case_requirements_remain_unverified_but_regressions_fail(self):
        binding = {'artifact': {'architecture': 'amd64'}}
        for failures, status in [([], 'unverified'), ([{'metric': 'peak_rss_bytes'}], 'failed')]:
            with self.subTest(failures=failures), \
                    patch.object(performance.platform, 'system', return_value='Linux'), \
                    patch.object(performance.platform, 'machine', return_value='x86_64'), \
                    patch.object(performance, 'resource_samples', return_value=({'failures': failures}, {})), \
                    patch.object(performance, 'retain', side_effect=lambda *args: args):
                result = performance.execute(binding, 'performance', 'memory-and-sustained-load', Path('/unused'))
                self.assertEqual(result[4], status)

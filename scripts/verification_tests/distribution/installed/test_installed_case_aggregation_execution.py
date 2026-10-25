"""Disjoint selected-case receipts combine only under the same exact authority."""
import copy
import json
import unittest
from unittest.mock import patch

from scripts.linux_release import installed_case_aggregation as owner


class SelectedCaseAggregationTests(unittest.TestCase):
    def setUp(self):
        self.selected = {'id': 'row', 'environment': 'noble', 'capability': 'session', 'cases': ['entry', 'cli']}
        self.variants = [{'id': 'floor'}]
        self.patch = patch.object(owner, 'requirements', return_value=self.variants)
        self.patch.start()
        self.addCleanup(self.patch.stop)

    def sample(self, selected, *, nonce='1', status='passed', variant='floor', uid='30001'):
        receipt = {'format_version': 1, 'execution_nonce': nonce * 32, 'source_commit': 'a' * 40,
                   'driver_tree': 'b' * 40, 'artifact_id': 'deb-amd64', 'environment_id': 'noble',
                   'variant_id': variant, 'case_selection': {'format_version': 1,
                       'cases': [{'capability': 'session', 'case': selected}]}}
        row = {'row_id': 'row', 'source_commit': 'a' * 40, 'artifact_id': 'deb-amd64',
               'tier': 'native-security', 'execution': 'native', 'status': 'unverified',
               'environment_facts': {'kernel': 'same-kernel',
                   'desktop-compositor-display-protocol': json.dumps({'User': uid, 'id': uid, 'Type': 'wayland'}),
                   'session-and-portal-provider-versions': json.dumps({'packages': 'same-snapshot',
                       'session': {'User': uid, 'id': uid, 'Type': 'wayland'}})},
               'cases': [{'id': key, 'status': status if key == selected else 'unverified',
                          'evidence': [{'path': nonce + '/' + key, 'sha256': 'c' * 64}]}
                         for key in self.selected['cases']],
               'variants': [{'id': variant, 'status': 'unverified', 'observed': {'snapshot': 'same', 'display_protocol': 'wayland'}}]}
        return variant, row, receipt

    def aggregate(self, samples):
        return owner.aggregate({}, {'row': samples}, {'row': self.selected})[0]

    def test_distinct_fresh_account_executions_complete_only_their_selected_cases(self):
        result = self.aggregate([self.sample('entry'), self.sample('cli', nonce='2', uid='30002')])
        self.assertEqual(result['status'], 'passed')
        self.assertEqual([case['status'] for case in result['cases']], ['passed', 'passed'])
        self.assertEqual(len(result['cases'][0]['evidence']), 1)
        identities = json.loads(result['environment_facts']['desktop-compositor-display-protocol'])['floor']
        self.assertEqual(set(identities), {'1' * 32, '2' * 32})
        self.assertEqual(result['variants'][0]['status'], 'passed')

    def test_missing_case_or_variant_never_becomes_a_pass(self):
        partial = self.aggregate([self.sample('entry')])
        self.assertEqual(partial['status'], 'unverified')
        self.assertEqual(partial['cases'][1]['status'], 'unverified')
        self.variants.append({'id': 'newer'})
        missing_variant = self.aggregate([self.sample('entry'), self.sample('cli', nonce='2')])
        self.assertEqual(missing_variant['status'], 'unverified')
        self.assertTrue(all(case['status'] == 'unverified' for case in missing_variant['cases']))

    def test_failure_is_retained_and_a_later_pass_cannot_replace_the_claim(self):
        failed = self.sample('entry', status='failed')
        result = self.aggregate([failed, self.sample('cli', nonce='2')])
        self.assertEqual(result['status'], 'failed')
        self.assertEqual(result['cases'][0]['status'], 'failed')
        with self.assertRaisesRegex(ValueError, 'overlap'):
            self.aggregate([failed, self.sample('entry', nonce='2')])

    def test_reused_nonce_and_changed_artifact_source_driver_or_environment_refuse(self):
        with self.assertRaisesRegex(ValueError, 'reuses'):
            self.aggregate([self.sample('entry'), self.sample('cli')])
        for field in ('source_commit', 'driver_tree', 'artifact_id', 'environment_id'):
            with self.subTest(field=field):
                second = self.sample('cli', nonce='2')
                second[2][field] = 'd' * 40 if field.endswith(('commit', 'tree')) else 'foreign'
                with self.assertRaisesRegex(ValueError, 'binding'):
                    self.aggregate([self.sample('entry'), second])

    def test_conflicting_platform_facts_or_unselected_claims_refuse(self):
        for field in ('environment_facts', 'variants'):
            second = self.sample('cli', nonce='2')
            if field == 'environment_facts':
                second[1][field]['kernel'] = 'different-kernel'
            else:
                second[1][field][0]['observed']['snapshot'] = 'different-snapshot'
            with self.assertRaisesRegex(ValueError, 'conflict'):
                self.aggregate([self.sample('entry'), second])
        second = self.sample('cli', nonce='2')
        second[1]['cases'][0]['status'] = 'passed'
        with self.assertRaisesRegex(ValueError, 'unselected'):
            self.aggregate([self.sample('entry'), second])

    def test_complete_variant_set_and_failed_variant_have_honest_precedence(self):
        self.variants.append({'id': 'newer'})
        samples = [self.sample('entry'), self.sample('cli', nonce='2'),
                   self.sample('entry', nonce='3', variant='newer'),
                   self.sample('cli', nonce='4', variant='newer')]
        self.assertEqual(self.aggregate(samples)['status'], 'passed')
        samples[-1][1]['cases'][1]['status'] = 'failed'
        self.assertEqual(self.aggregate(samples)['status'], 'failed')
        with self.assertRaisesRegex(ValueError, 'invents'):
            self.aggregate([self.sample('entry', variant='invented')])

    def test_invalid_or_repeated_case_results_refuse(self):
        for mutate in (lambda row: row['cases'].pop(), lambda row: row['cases'].append(copy.deepcopy(row['cases'][0]))):
            sample = self.sample('entry')
            mutate(sample[1])
            with self.assertRaisesRegex(ValueError, 'omitted or repeated'):
                self.aggregate([sample])

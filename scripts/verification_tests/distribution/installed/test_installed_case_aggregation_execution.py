"""Disjoint selected-case receipts combine only under the same exact authority."""
import copy
import json
import unittest
from unittest.mock import patch

from scripts.linux_release import installed_case_aggregation as owner


class SelectedCaseAggregationTests(unittest.TestCase):
    def setUp(self):
        self.selected = {'id': 'row', 'environment': 'noble', 'capability': 'session', 'cases': ['entry', 'cli'],
                         'tier': 'native-security', 'emulation_allowed': False}
        self.variants = [{'id': 'floor', 'release': {'id': 'ubuntu-floor', 'os_ids': ['ubuntu'], 'version_id': '24.04',
            'snapshot_kind': 'package-inventory', 'channel': None}, 'display': {'protocol': 'wayland'}, 'scales_percent': [100]}]
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
               'variants': [{'id': variant, 'status': 'unverified', 'observed': {'os_id': 'ubuntu', 'version_id': '24.04',
                   'snapshot': {'kind': 'package-inventory', 'value': 'd' * 64, 'channel': None},
                   'display_protocol': 'wayland', 'scales_percent': [100]}}]}
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
        self.variants.append({**copy.deepcopy(self.variants[0]), 'id': 'newer'})
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
                second[1][field][0]['observed']['snapshot']['value'] = 'e' * 64
            with self.assertRaisesRegex(ValueError, 'conflict'):
                self.aggregate([self.sample('entry'), second])
        second = self.sample('cli', nonce='2')
        second[1]['cases'][0]['status'] = 'passed'
        with self.assertRaisesRegex(ValueError, 'unselected'):
            self.aggregate([self.sample('entry'), second])

    def test_complete_variant_set_and_failed_variant_have_honest_precedence(self):
        self.variants.append({**copy.deepcopy(self.variants[0]), 'id': 'newer'})
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

    def test_explicit_row_and_variant_failures_are_not_erased_by_passed_selected_cases(self):
        for failed in ('row', 'variant'):
            with self.subTest(failed=failed):
                second = self.sample('cli', nonce='2')
                if failed == 'row':
                    second[1]['status'] = 'failed'
                else:
                    second[1]['variants'][0]['status'] = 'failed'
                result = self.aggregate([self.sample('entry'), second])
                self.assertEqual(result['status'], 'failed')
                self.assertEqual(result['variants'][0]['status'], 'failed')
                self.assertTrue(all(case['status'] == 'passed' for case in result['cases']))

    def test_tier_execution_and_native_floor_cannot_change_within_or_across_variants(self):
        self.variants.append({**copy.deepcopy(self.variants[0]), 'id': 'newer'})
        for variant in ('floor', 'newer'):
            for field, value in (('tier', 'installed-functional'), ('execution', 'emulated')):
                with self.subTest(variant=variant, field=field):
                    second = self.sample('cli', nonce='2', variant=variant)
                    second[1][field] = value
                    with self.assertRaisesRegex(ValueError, 'tier/execution'):
                        self.aggregate([self.sample('entry'), second])
        # Even an emulation-allowed row cannot relabel a later emulated sample as native.
        self.selected['emulation_allowed'] = True
        second = self.sample('cli', nonce='2', variant='newer')
        second[1]['execution'] = 'emulated'
        with self.assertRaisesRegex(ValueError, 'tier/execution'):
            self.aggregate([self.sample('entry'), second])

    def test_failed_required_variant_remains_failed_when_all_cases_pass(self):
        self.variants.append({**copy.deepcopy(self.variants[0]), 'id': 'newer'})
        samples = [self.sample('entry'), self.sample('cli', nonce='2'),
                   self.sample('entry', nonce='3', variant='newer'), self.sample('cli', nonce='4', variant='newer')]
        samples[-1][1]['variants'][0]['status'] = 'failed'
        result = self.aggregate(samples)
        self.assertEqual(result['status'], 'failed')
        self.assertEqual(result['variants'][1]['status'], 'failed')

    def test_catalog_platform_mismatch_cannot_be_promoted_by_case_passes(self):
        samples = [self.sample('entry'), self.sample('cli', nonce='2')]
        for _, row, _ in samples:
            row['variants'][0]['observed']['scales_percent'] = [125]
        result = self.aggregate(samples)
        self.assertEqual(result['status'], 'unverified')
        self.assertEqual(result['variants'][0]['status'], 'unverified')
        self.assertTrue(all(case['status'] == 'passed' for case in result['cases']))

    def test_one_snapshot_cannot_cover_distinct_declared_releases(self):
        newer = {**copy.deepcopy(self.variants[0]), 'id': 'newer'}
        newer['release']['id'] = 'ubuntu-newer'
        self.variants.append(newer)
        samples = [self.sample('entry'), self.sample('cli', nonce='2'),
                   self.sample('entry', nonce='3', variant='newer'), self.sample('cli', nonce='4', variant='newer')]
        self.assertEqual(self.aggregate(samples)['status'], 'unverified')

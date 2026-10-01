import copy
import unittest
from roster_revision import replace_candidate, retained_records


class RosterRevisionTests(unittest.TestCase):
    def setUp(self):
        common = {'engine_sha256': 'frozen', 'lite': {'provider': 'cloud'},
                  'workers': [{'provider': 'cloud'}]}
        self.old = {'id': 'local', 'provider': 'local', 'model': 'old',
                    'configuration': {**common, 'coordinator': {'provider': 'local'}, 'configuration_sha256': 'old'}}
        self.cloud = {'id': 'cloud', 'provider': 'cloud', 'model': 'stable',
                      'configuration': {**common, 'coordinator': {'provider': 'cloud'}, 'configuration_sha256': 'stable'}}
        self.new = {'id': 'replacement', 'provider': 'cloud', 'model': 'new',
                    'configuration': {**common, 'coordinator': {'provider': 'cloud'}, 'configuration_sha256': 'new'}}
        policy = {'max_active': 5, 'resources': [
            {'id': 'p0', 'providers': ['local'], 'max_active': 1, 'max_coordinators': 1},
            {'id': 'p1', 'providers': ['cloud'], 'max_active': 5, 'max_coordinators': 2}],
            'models': {'local': {'coordinator': 'p0', 'resources': ['p0', 'p1']},
                       'cloud': {'coordinator': 'p1', 'resources': ['p1']}}}
        self.plan = {'run_id': 'original', 'comparison_id': 'original',
                     'models': [self.old, self.cloud], 'roster': {'models': [self.old, self.cloud]},
                     'episodes': [{'index': 0, 'model': 'local', 'case': 'one', 'repeat': 0},
                                  {'index': 1, 'model': 'cloud', 'case': 'one', 'repeat': 0},
                                  {'index': 2, 'model': 'local', 'case': 'two', 'repeat': 0}],
                     'execution': {'policy': policy, 'driver_sha256': 'frozen'}, 'benchmark': {}}

    def test_replacement_preserves_originals_and_uses_fresh_slots(self):
        before = copy.deepcopy(self.plan)
        revised = replace_candidate(self.plan, 'local', self.new)
        self.assertEqual(self.plan, before)
        self.assertEqual(revised['models'][1], self.cloud)
        self.assertEqual(revised['episodes'][1], self.plan['episodes'][1])
        self.assertEqual([i['index'] for i in revised['episodes']], [3, 1, 4])
        self.assertEqual([(i['case'],i['repeat']) for i in revised['episodes']],
                         [(i['case'],i['repeat']) for i in self.plan['episodes']])
        self.assertEqual(revised['execution']['policy']['resources'], [self.plan['execution']['policy']['resources'][1]])
        self.assertEqual(revised['execution']['driver_sha256'], 'frozen')
        self.assertNotEqual(revised['run_id'], self.plan['run_id'])
        records = {'0': {'report': 'local'}, '1': {'report': 'cloud'}}
        self.assertEqual(retained_records(self.plan, revised, records), {'1': records['1']})

    def test_changed_app_or_support_cannot_reuse_results(self):
        changed = copy.deepcopy(self.new)
        changed['configuration']['engine_sha256'] = 'changed'
        with self.assertRaisesRegex(ValueError, 'application or supporting'):
            replace_candidate(self.plan, 'local', changed)

    def test_existing_candidate_identity_cannot_be_reused(self):
        with self.assertRaisesRegex(ValueError, 'new candidate identity'):
            replace_candidate(self.plan, 'local', self.old)

    def test_unknown_provider_capacity_is_rejected(self):
        changed = copy.deepcopy(self.new)
        changed['configuration']['coordinator']['provider'] = 'unknown'
        with self.assertRaisesRegex(ValueError, 'unplanned provider capacity'):
            replace_candidate(self.plan, 'local', changed)

    def test_completed_slot_cannot_be_reassigned(self):
        changed = copy.deepcopy(self.plan)
        changed['episodes'][1]['model'] = 'local'
        with self.assertRaisesRegex(ValueError, 'identity changed'):
            retained_records(self.plan, changed, {'1': {'report': 'cloud'}})


if __name__ == '__main__':
    unittest.main()

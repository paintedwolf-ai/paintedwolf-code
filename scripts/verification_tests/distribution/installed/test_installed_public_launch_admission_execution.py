"""Public launch cases cannot substitute controlled scope or share OS input custody."""
import copy
import json
from pathlib import Path
import tempfile
import unittest

from scripts.linux_release.installed_cases import shipping_lifecycle as routes
from scripts.linux_release import installed_execution
from scripts.linux_release.installed_input import controller
from scripts.linux_release.support_matrix import CATALOG


class PublicLaunchAdmissionTests(unittest.TestCase):
    def test_catalog_requires_distinct_public_routes_and_fixed_executor_modes(self):
        catalog = json.loads(CATALOG.read_text())
        cases = next(item['cases'] for item in catalog['capabilities'] if item['id'] == 'session')
        self.assertNotIn('inhibitor-lifetime', cases)
        for case, mode in routes.PUBLIC_CASE_MODES.items():
            self.assertIn(case, cases)
            self.assertEqual(installed_execution.case_family('session', case, 'deb'), 'keepawake')
            self.assertEqual(routes.selected_mode({}, case), mode)
            with self.assertRaisesRegex(ValueError, 'substitute'):
                routes.selected_mode({'case_inputs': {'desktop': {'sleep_launch_mode': 'controlled'}}}, case)
        self.assertEqual(routes.selected_mode({}), 'controlled')

    def test_second_turn_input_follows_initial_turn_and_lock_without_replaying_registration(self):
        operations = ['provider-name-v1', 'provider-endpoint-v1-18765', 'provider-fixture-credential-v1',
                      'reply-fixture-prompt-v1', 'reply-fixture-prompt-v1']
        transactions = [{'operation': name, 'transaction_id': str(index + 1) * 32,
                         'events': []} for index, name in enumerate(operations)]
        observed = {'launch': {'fixed_input': transactions},
                    'lock_transition': {'operation': 'session-lock-v1', 'transaction_id': '6' * 32, 'events': []}}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            leaf = root / 'leaf/evidence/session/inhibitor-desktop-entry-lifetime/execution.json'
            leaf.parent.mkdir(parents=True)
            leaf.write_text(json.dumps({'observations': observed}))
            records, declared = controller.delivery_records(root, {}, 'owned')
            self.assertEqual(declared, [*operations[:4], 'session-lock-v1', operations[-1]])
            self.assertEqual([record['transaction_id'] for record in records], ['1' * 32, '2' * 32,
                '3' * 32, '4' * 32, '6' * 32, '5' * 32])
            replayed = copy.deepcopy(observed)
            replayed['native_input'] = transactions[-1]
            leaf.write_text(json.dumps({'observations': replayed}))
            with self.assertRaisesRegex(ValueError, 'repeats'):
                controller.delivery_records(root, {}, 'owned')
            early = copy.deepcopy(observed)
            del early['lock_transition']
            leaf.write_text(json.dumps({'observations': early}))
            with self.assertRaisesRegex(ValueError, 'precedes'):
                controller.delivery_records(root, {}, 'owned')

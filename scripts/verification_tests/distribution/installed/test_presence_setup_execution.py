"""A failed secret prerequisite retains its mutation boundary without replay."""
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases import presence_setup
from linux_release.installed_cases.records import CaseUnavailable


class PresenceSecretSetupTests(unittest.TestCase):
    def setUp(self):
        self.app = SimpleNamespace(observations={})
        self.ui, self.reservation = Mock(), Mock()
        self.ui.api.ROLE_PUSH_BUTTON = 43
        self.navigation = patch.object(presence_setup, 'open_project_configuration', return_value={'owned': True})
        self.inputs = patch.object(presence_setup, 'physical_input')
        self.alerts = patch.object(presence_setup, 'alerts', return_value=[])
        self.navigation.start()
        self.input = self.inputs.start()
        self.alert = self.alerts.start()
        self.addCleanup(patch.stopall)

    def test_ready_requires_reveal_observation_after_one_create_action(self):
        facts = presence_setup.prepare_secret(self.app, self.ui, self.reservation, '/fixture', {'id': 'session', 'project_id': 'project'})
        self.assertEqual(facts['phase'], 'ready')
        self.assertTrue(facts['create_action_completed'])
        self.assertTrue(facts['reveal_ready'])
        self.ui.find.assert_called_once()
        self.assertEqual(self.input.call_count, 3)
        self.assertEqual([c.args[0] for c in self.ui.click.call_args_list], ['Secrets', 'Add secret', 'Add secret'])
        self.assertNotIn('value', facts)

    def test_uncertain_create_is_not_replayed_and_never_claimed_ready(self):
        self.ui.click.side_effect = [None, None, CaseUnavailable('uncertain delivery')]
        with self.assertRaises(CaseUnavailable):
            presence_setup.prepare_secret(self.app, self.ui, self.reservation, '/fixture', {'id': 'session', 'project_id': 'project'})
        facts = self.app.observations['presence_secret_setup']
        self.assertEqual(facts['phase'], 'create-dispatch')
        self.assertFalse(facts['create_action_completed'])
        self.assertFalse(facts['reveal_ready'])
        self.assertEqual(self.ui.click.call_count, 3)
        self.ui.find.assert_not_called()

    def test_missing_reveal_preserves_acknowledged_create_without_recreation(self):
        self.ui.find.side_effect = CaseUnavailable('reveal missing')
        with self.assertRaises(CaseUnavailable):
            presence_setup.prepare_secret(self.app, self.ui, self.reservation, '/fixture', {'id': 'session', 'project_id': 'project'})
        facts = self.app.observations['presence_secret_setup']
        self.assertEqual(facts['phase'], 'reveal-observation')
        self.assertTrue(facts['create_action_completed'])
        self.assertFalse(facts['reveal_ready'])
        self.assertEqual(self.ui.click.call_count, 3)

    def test_preexisting_alert_prevents_presence_policy_from_being_ready(self):
        self.alert.return_value = [{'role': 'alert'}]
        with self.assertRaises(CaseUnavailable):
            presence_setup.prepare_secret(self.app, self.ui, self.reservation, '/fixture', {'id': 'session', 'project_id': 'project'})
        self.assertEqual(self.app.observations['presence_secret_setup']['phase'], 'existing-alert')
        self.assertFalse(self.app.observations['presence_secret_setup']['reveal_ready'])

    def test_second_attempt_cannot_recreate_an_acknowledged_secret(self):
        presence_setup.prepare_secret(self.app, self.ui, self.reservation, '/fixture', {'id': 'session', 'project_id': 'project'})
        before = list(self.ui.mock_calls)
        with self.assertRaisesRegex(CaseUnavailable, 'already-attempted'):
            presence_setup.prepare_secret(self.app, self.ui, self.reservation, '/fixture', {'id': 'session', 'project_id': 'project'})
        self.assertEqual(self.ui.mock_calls, before)

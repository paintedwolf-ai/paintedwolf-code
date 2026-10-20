"""UI provider setup requires public form actions and a matching host identity."""
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import conversation


class InstalledConversationTests(unittest.TestCase):
    def record(self, **changes):
        return dict({'id': 'fixture', 'label': 'selected', 'kind': 'openai-compatible',
                     'base_url': 'http://127.0.0.1:123/v1', 'configured': True,
                     'requires_api_key': False, 'credential_present': False}, **changes)

    def test_only_exact_keyless_selected_provider_is_witness(self):
        row = self.record()
        with patch.object(conversation, 'accepted', return_value={'providers': [row]}):
            result = conversation.provider_record(None, 'selected', row['base_url'])
            self.assertEqual(result['provider_id'], 'fixture')
            self.assertNotIn('base_url', result)
        for changes in ({'kind': 'other'}, {'requires_api_key': True}, {'credential_present': True},
                        {'configured': False}, {'base_url': 'https://other.invalid'}):
            with patch.object(conversation, 'accepted', return_value={'providers': [self.record(**changes)]}):
                with self.assertRaises(ValueError):
                    conversation.provider_record(None, 'selected', row['base_url'])

    def test_missing_provider_is_not_completion_and_duplicate_is_refused(self):
        with patch.object(conversation, 'accepted', return_value={'providers': []}):
            self.assertIsNone(conversation.provider_record(None, 'selected', 'endpoint'))
        with patch.object(conversation, 'accepted', return_value={'providers': [self.record(), self.record()]}):
            with self.assertRaises(ValueError):
                conversation.provider_record(None, 'selected', 'endpoint')

    def test_ui_uses_normal_exact_labeled_controls_before_host_witness(self):
        ui = MagicMock()
        ui.api.ROLE_ENTRY = 7
        ui.__enter__.return_value = ui
        fixture = SimpleNamespace(base_url='http://127.0.0.1:123/v1')
        with patch.object(conversation, 'DesktopUI', return_value=ui), \
                patch.object(conversation, 'provider_record', return_value={'provider_id': 'actual'}) as witness:
            self.assertEqual(conversation.add_keyless_provider(None, fixture), {'provider_id': 'actual'})
        self.assertEqual([call.args for call in ui.click.call_args_list],
                         [('Add AI provider',), ('OpenAI-compatible',), ('Add AI provider',)])
        self.assertEqual(ui.fill.call_args_list[0].args[:2], (7, 'Name (optional)'))
        self.assertEqual(ui.fill.call_args_list[1].args, (7, 'Endpoint URL', fixture.base_url))
        self.assertEqual(witness.call_args.args[2], fixture.base_url)

    def test_provider_inventory_is_typed_and_bounded(self):
        for rows in (None, [None], [{}] * 257):
            with patch.object(conversation, 'accepted', return_value={'providers': rows}):
                with self.assertRaises(ValueError):
                    conversation.provider_record(None, 'selected', 'endpoint')

    def test_optional_setup_action_never_skips_owned_provider_cleanup(self):
        app, ui, provider = MagicMock(), MagicMock(), MagicMock()
        app.__enter__.return_value = app
        ui.__enter__.return_value = ui
        provider.__enter__.return_value = provider
        app.request.return_value = {'status': 204}
        with patch.object(conversation, 'InstalledApp', return_value=app), \
                patch.object(conversation, 'ProviderFixture', return_value=provider), \
                patch.object(conversation, 'DesktopUI', return_value=ui), \
                patch.object(conversation, 'interactive_window', return_value={}), \
                patch.object(conversation, 'add_keyless_provider', return_value={'provider_id': 'owned'}), \
                patch.object(conversation, 'retain', side_effect=lambda *args: args) as retained:
            ui.click.side_effect = conversation.CaseUnavailable('no optional control')
            conversation.execute({}, 'conversation', 'provider-onboarding', None)
            self.assertEqual(retained.call_args.args[4], 'unverified')
            self.assertFalse(retained.call_args.args[5]['normal_optional_setup_skip'])
            app.request.assert_called_with('DELETE', '/v1/providers/owned')
            app.request.reset_mock()
            ui.click.side_effect = ValueError('owner changed')
            with self.assertRaises(ValueError):
                conversation.execute({}, 'conversation', 'provider-onboarding', None)
            app.request.assert_called_with('DELETE', '/v1/providers/owned')

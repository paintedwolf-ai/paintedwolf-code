"""The shared project turn uses one closed normal composer transaction."""
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, Mock, patch

from linux_release.installed_cases import desktop_turn as owner
from linux_release.installed_cases import settings_query
from linux_release.installed_cases.records import CaseUnavailable

ROOT = {'pid': 10, 'parent_pid': 2, 'start_time_ticks': 1}
FRAME = {'pid': 11, 'parent_pid': 10, 'start_time_ticks': 2}
WEB = {'pid': 12, 'parent_pid': 11, 'start_time_ticks': 3}


class DesktopTurnTests(unittest.TestCase):
    def test_fixed_operation_preserves_physical_input_then_normal_send(self):
        app, ui = object(), MagicMock()
        ui.__enter__.return_value = ui
        record = {'operation': 'reply-fixture-prompt-v1'}
        order = []
        with patch.object(owner, 'DesktopUI', return_value=ui) as desktop, \
                patch.object(owner, 'first_time_tips_enabled', return_value=False), \
                patch.object(owner, 'physical_input', side_effect=lambda *args: order.append('input') or record) as physical:
            ui.click.side_effect = lambda label: order.append(f'click:{label}')
            self.assertIs(owner.submit_project_turn(app), record)
        desktop.assert_called_once_with(app)
        physical.assert_called_once_with(app, ui, 'What would you like to work on?', 'reply-fixture-prompt-v1')
        ui.click.assert_called_once_with('Send message')
        self.assertEqual(order, ['input', 'click:Send message'])
        ui.__exit__.assert_called_once()

    def test_first_time_tips_are_disabled_and_persisted_before_composer_input(self):
        app, ui = object(), MagicMock()
        ui.__enter__.return_value = ui
        record = {'operation': 'reply-fixture-prompt-v1'}
        order = []
        with patch.object(owner, 'DesktopUI', return_value=ui), \
                patch.object(owner, 'first_time_tips_enabled', side_effect=[True, True, False]) as tips, \
                patch.object(owner.time, 'sleep') as sleep, \
                patch.object(owner, 'physical_input', side_effect=lambda *args: order.append('input') or record):
            ui.click.side_effect = lambda label: order.append(f'click:{label}')
            self.assertIs(owner.submit_project_turn(app), record)
        self.assertEqual(tips.call_count, 3)
        sleep.assert_called_once_with(0.05)
        self.assertEqual(order, ['click:Disable tips', 'input', 'click:Send message'])
        with patch.object(owner, 'first_time_tips_enabled', return_value=True), \
                patch.object(owner.time, 'monotonic', side_effect=[0.0, 10.0]), \
                patch.object(owner.time, 'sleep'):
            with self.assertRaisesRegex(owner.CaseUnavailable, 'normal-first-time-tips-disable-did-not-persist'):
                owner.prepare_composer(app, ui)

    def test_other_input_operations_are_refused_before_ui(self):
        with patch.object(owner, 'DesktopUI') as desktop:
            for operation in ('provider-name-v1', 'arbitrary', None):
                with self.subTest(operation=operation), self.assertRaises(ValueError):
                    owner.submit_project_turn(object(), operation=operation)
            desktop.assert_not_called()

    def test_failed_physical_transaction_never_sends(self):
        ui = MagicMock()
        ui.__enter__.return_value = ui
        with patch.object(owner, 'DesktopUI', return_value=ui), \
                patch.object(owner, 'first_time_tips_enabled', return_value=False), \
                patch.object(owner, 'physical_input', side_effect=ValueError('refused')):
            with self.assertRaises(ValueError):
                owner.submit_project_turn(object())
        ui.click.assert_not_called()

    def configuration_fixture(self):
        app = SimpleNamespace(observations={'launch_identity': ROOT})
        frame = Mock(get_process_id=Mock(return_value=11))
        api = SimpleNamespace(ROLE_COMBO_BOX=11, ROLE_MENU_ITEM=35, ROLE_PAGE_TAB=37,
                              ROLE_PUSH_BUTTON=43, STATE_FOCUSED=12)
        labels = ['App menu', 'Go', 'Crossbar…', 'Settings: AI providers Other · painted-wolf/platform', 'Alpha → AI providers']
        nodes = []
        for label in labels:
            node = Mock(get_process_id=Mock(return_value=12))
            node.name = label
            node.queryAction.return_value = SimpleNamespace(nActions=1, doAction=Mock(return_value=True))
            node.queryComponent.return_value.grabFocus.return_value = True
            node.getState.return_value.contains.return_value = True
            nodes.append(node)
        ui = Mock(api=api, find=Mock(side_effect=nodes[:3] + nodes[4:]), visible=Mock(return_value=True),
                  settings_provider_command=Mock(return_value=nodes[3]),
                  settings_provider_command_matches=Mock(return_value=True))
        ui.windows.return_value = []
        target = {'project_id': 'project', 'session_id': 'session', 'root_id': 'root', 'display_name': 'Alpha'}
        chain = [{'bus_name': ':1.2', 'path': '/control', 'pid': 12},
                 {'bus_name': ':1.1', 'path': '/frame', 'pid': 11}]
        return app, ui, frame, nodes, target, chain

    def configuration_patches(self, frame, chain):
        from contextlib import ExitStack
        stack = ExitStack()
        self.addCleanup(stack.close)
        stack.enter_context(patch.object(owner, 'prepare_composer'))
        stack.enter_context(patch.object(owner, 'process_identity', side_effect=lambda pid: {11: FRAME, 12: WEB}[pid]))
        stack.enter_context(patch.object(owner.accessibility, 'capture', return_value=(frame, chain)))
        stack.enter_context(patch.object(owner.lineage, 'capture', side_effect=lambda r, c: [c, r]))
        stack.enter_context(patch.object(owner.time, 'sleep'))
        query = Mock()
        query.enter.return_value = {'operation': 'settings-providers-query-v1', 'completed': True}
        stack.enter_context(patch.object(owner, 'SettingsQuery', return_value=query))
        return stack

    def test_configuration_route_uses_exact_selected_project_and_owned_normal_actions(self):
        app, ui, frame, nodes, target, chain = self.configuration_fixture()
        stack = self.configuration_patches(frame, chain)
        observed = stack.enter_context(patch.object(owner, 'project_configuration_identity', return_value=target))
        witness = owner.open_project_configuration(app, ui, '/fixture', {'id': 'session', 'project_id': 'project'})
        self.assertEqual([c.args for c in ui.find.call_args_list], [
            (11, 'App menu'), (35, 'Go'), (35, 'Crossbar…'), (43, 'Alpha → AI providers')])
        self.assertEqual(len({c.kwargs['deadline'] for c in ui.find.call_args_list}), 1)
        self.assertEqual(observed.call_count, 13)
        ui.settings_provider_command.assert_called_once()
        self.assertEqual(ui.settings_provider_command.call_args.args[0], ui.find.call_args_list[0].kwargs['deadline'])
        self.assertFalse(app.observations['project_configuration_query']['accessibility_exposes_catalog_id'])
        for node in nodes:
            node.queryAction.return_value.doAction.assert_called_once_with(0)
        for node in nodes[1:3]:
            node.queryComponent.return_value.grabFocus.assert_called_once_with()
        self.assertEqual(witness['project'], target)
        self.assertEqual(witness['actions'][-1]['accessible_chain'], chain)
        self.assertEqual(witness['actions'][-1]['frame_lineage'], [FRAME, ROOT])
        self.assertEqual(witness['actions'][-1]['entry_lineage'], [WEB, FRAME])

    def test_configuration_route_refuses_changed_selection_before_further_action(self):
        app, ui, frame, nodes, target, chain = self.configuration_fixture()
        stack = self.configuration_patches(frame, chain)
        stack.enter_context(patch.object(owner, 'project_configuration_identity', side_effect=[
            target, target, {**target, 'project_id': 'foreign'}]))
        with self.assertRaisesRegex(ValueError, 'selected project changed'):
            owner.open_project_configuration(app, ui, '/fixture', {'id': 'session', 'project_id': 'project'})
        nodes[0].queryAction.return_value.doAction.assert_called_once_with(0)
        ui.find.assert_called_once()
        self.assertEqual(len(app.observations['project_configuration_actions']), 1)

    def test_configuration_route_retains_uncertain_dispatch_without_replay(self):
        app, ui, frame, nodes, target, chain = self.configuration_fixture()
        stack = self.configuration_patches(frame, chain)
        stack.enter_context(patch.object(owner, 'project_configuration_identity', return_value=target))
        retry = stack.enter_context(patch.object(owner, 'retry_inventory', return_value=True))
        nodes[1].queryAction.return_value.doAction.side_effect = RuntimeError('after dispatch')
        with self.assertRaises(CaseUnavailable) as refusal:
            owner.open_project_configuration(app, ui, '/fixture', {'id': 'session', 'project_id': 'project'})
        retry.assert_not_called()
        nodes[1].queryAction.return_value.doAction.assert_called_once_with(0)
        self.assertEqual(len(app.observations['project_configuration_actions']), 1)
        self.assertEqual(refusal.exception.observations['project_configuration_mutation']['phase'], 'do-action')

    def test_configuration_route_ownership_change_refuses_before_action(self):
        app, ui, frame, nodes, target, chain = self.configuration_fixture()
        stack = self.configuration_patches(frame, chain)
        stack.enter_context(patch.object(owner, 'project_configuration_identity', return_value=target))
        stack.enter_context(patch.object(owner.accessibility, 'capture', side_effect=[
            (frame, chain), (frame, [{**chain[0], 'pid': 99}])]))
        with self.assertRaisesRegex(ValueError, 'identity changed'):
            owner.open_project_configuration(app, ui, '/fixture', {'id': 'session', 'project_id': 'project'})
        nodes[0].queryAction.return_value.doAction.assert_not_called()

    def test_configuration_route_never_selects_a_command_after_incomplete_query(self):
        app, ui, frame, nodes, target, chain = self.configuration_fixture()
        stack = self.configuration_patches(frame, chain)
        stack.enter_context(patch.object(owner, 'project_configuration_identity', return_value=target))
        query = Mock()
        query.enter.return_value = {'operation': 'settings-providers-query-v1', 'completed': False}
        constructor = stack.enter_context(patch.object(owner, 'SettingsQuery', return_value=query))
        with self.assertRaisesRegex(ValueError, 'fixed operation'):
            owner.open_project_configuration(app, ui, '/fixture', {'id': 'session', 'project_id': 'project'})
        ui.settings_provider_command.assert_not_called()
        self.assertEqual(constructor.call_args.args[2], ui.find.call_args_list[0].kwargs['deadline'])

    def test_query_uncertainty_before_command_dispatch_never_selects_or_replays(self):
        for failure in (CaseUnavailable('readback unavailable'), ValueError('identity changed')):
            app, ui, frame, nodes, target, chain = self.configuration_fixture()
            stack = self.configuration_patches(frame, chain)
            stack.enter_context(patch.object(owner, 'project_configuration_identity', return_value=target))
            query = Mock()
            query.enter.return_value = {'operation': 'settings-providers-query-v1', 'completed': True}
            query.require_current.side_effect = failure
            constructor = stack.enter_context(patch.object(owner, 'SettingsQuery', return_value=query))
            with self.assertRaises(type(failure)):
                owner.open_project_configuration(app, ui, '/fixture', {'id': 'session', 'project_id': 'project'})
            query.enter.assert_called_once_with()
            query.require_current.assert_called_once_with()
            nodes[3].queryAction.return_value.doAction.assert_not_called()
            nodes[4].queryAction.return_value.doAction.assert_not_called()
            self.assertEqual(constructor.call_args.args[2], ui.find.call_args_list[0].kwargs['deadline'])


class ClosedSettingsQueryTests(unittest.TestCase):
    def fixture(self):
        app = SimpleNamespace(binding={'native_input': {'binding': {'fixed': True}, 'vm_id': 'owned-vm'}}, observations={})
        text = Mock(characterCount=len('> Settings: AI providers'))
        text.getText.return_value = '> Settings: AI providers'
        node = Mock()
        node.queryText.return_value = text
        actor = Mock(node=node)
        actor.observe.return_value = {'owned': True}
        patcher = patch.object(settings_query, 'EntryFocus', return_value=actor)
        constructor = patcher.start()
        self.addCleanup(patcher.stop)
        query = settings_query.SettingsQuery(app, object(), settings_query.time.monotonic() + 30)
        return app, query, actor, text, constructor

    def test_only_fixed_public_query_span_is_read_and_no_text_is_retained(self):
        app, query, actor, text, constructor = self.fixture()
        record = {'operation': 'settings-providers-query-v1', 'completed': True}
        with patch.object(settings_query, 'perform', return_value=record) as perform:
            self.assertIs(query.enter(), record)
        constructor.assert_called_once()
        self.assertEqual(constructor.call_args.args[2], 'Crossbar…')
        self.assertEqual(constructor.call_args.kwargs, {'deadline': query.deadline})
        perform.assert_called_once_with({'fixed': True}, 'owned-vm', actor,
                                       operation='settings-providers-query-v1', deadline=query.deadline)
        text.getText.assert_called_once_with(0, len('> Settings: AI providers'))
        self.assertEqual(app.observations['project_configuration_query_readback'],
                         {'fixed_query_matches': True, 'explicit_actions_arm': True})
        self.assertNotIn('Settings:', repr(app.observations))

    def test_wrong_fixed_query_or_missing_actions_arm_is_refused_without_export(self):
        for value in ('? Settings: AI providers', 'Settings: AI providers', '> Settings: ai providers'):
            app, query, actor, text, _ = self.fixture()
            text.characterCount = len(value)
            text.getText.return_value = value
            with self.assertRaisesRegex(CaseUnavailable, 'fixed-query-mismatch'):
                query.require_current()
            self.assertEqual(app.observations['project_configuration_query_readback'],
                             {'fixed_query_matches': False, 'explicit_actions_arm': False})
            self.assertNotIn(value, repr(app.observations))
            if len(value) != len('> Settings: AI providers'):
                text.getText.assert_not_called()

    def test_incomplete_or_changed_ownership_never_confirms_query(self):
        for failure in (CaseUnavailable('inventory incomplete'), ValueError('identity changed')):
            app, query, actor, text, _ = self.fixture()
            actor.observe.side_effect = failure
            with self.assertRaises(type(failure)):
                query.require_current()
            text.getText.assert_not_called()
            self.assertFalse(app.observations['project_configuration_query_readback']['fixed_query_matches'])
        app, query, actor, text, _ = self.fixture()
        actor.observe.side_effect = [{'owner': 1}, {'owner': 2}]
        with self.assertRaisesRegex(ValueError, 'identity changed'):
            query.require_current()
        self.assertFalse(app.observations['project_configuration_query_readback']['fixed_query_matches'])

    def test_readback_transport_uncertainty_and_deadline_never_confirm_or_replay(self):
        app, query, actor, text, _ = self.fixture()
        text.getText.side_effect = RuntimeError('unavailable')
        with self.assertRaisesRegex(CaseUnavailable, 'readback-unavailable'):
            query.require_current()
        self.assertFalse(app.observations['project_configuration_query_readback']['fixed_query_matches'])
        text.getText.reset_mock()
        with patch.object(settings_query.time, 'monotonic', return_value=query.deadline):
            with self.assertRaisesRegex(CaseUnavailable, 'query-deadline'):
                query.require_current()
        text.getText.assert_not_called()

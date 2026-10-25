"""The shared project turn uses one closed normal composer transaction."""
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, Mock, patch

from linux_release.installed_cases import desktop_turn as owner
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
        labels = ['App menu', 'Painted Wolf Code', 'Settings…', 'AI providers', 'Alpha → AI providers']
        nodes = []
        for label in labels:
            node = Mock(get_process_id=Mock(return_value=12))
            node.name = label
            node.queryAction.return_value = SimpleNamespace(nActions=1, doAction=Mock(return_value=True))
            node.queryComponent.return_value.grabFocus.return_value = True
            node.getState.return_value.contains.return_value = True
            nodes.append(node)
        ui = Mock(api=api, find=Mock(side_effect=nodes), visible=Mock(return_value=True))
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
        return stack

    def test_configuration_route_uses_exact_selected_project_and_owned_normal_actions(self):
        app, ui, frame, nodes, target, chain = self.configuration_fixture()
        stack = self.configuration_patches(frame, chain)
        observed = stack.enter_context(patch.object(owner, 'project_configuration_identity', return_value=target))
        witness = owner.open_project_configuration(app, ui, '/fixture', {'id': 'session', 'project_id': 'project'})
        self.assertEqual([c.args for c in ui.find.call_args_list], [
            (11, 'App menu'), (35, 'Painted Wolf Code'), (35, 'Settings…'),
            (37, 'AI providers'), (43, 'Alpha → AI providers')])
        self.assertEqual(len({c.kwargs['deadline'] for c in ui.find.call_args_list}), 1)
        self.assertEqual(observed.call_count, 11)
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

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

    def test_project_configuration_uses_owned_public_control_and_retains_visible_witness(self):
        app = SimpleNamespace(observations={'launch_identity': ROOT})
        frame = Mock(get_process_id=Mock(return_value=11))
        node = Mock(get_process_id=Mock(return_value=12))
        action = SimpleNamespace(nActions=1, doAction=Mock(return_value=True))
        node.queryAction.side_effect = [NotImplementedError('mounting'), action]
        ui = Mock(api=SimpleNamespace(ROLE_PUSH_BUTTON=43), find=Mock(return_value=node))
        ui.windows.return_value = []
        chain = [{'bus_name': ':1.2', 'path': '/control', 'pid': 12},
                 {'bus_name': ':1.1', 'path': '/frame', 'pid': 11}]
        with patch.object(owner, 'prepare_composer') as prepare, \
                patch.object(owner, 'process_identity', side_effect=lambda pid: {11: FRAME, 12: WEB}[pid]), \
                patch.object(owner.accessibility, 'capture', return_value=(frame, chain)), \
                patch.object(owner.lineage, 'capture', side_effect=lambda r, c: [c, r]), \
                patch.object(owner, 'retry_inventory', return_value=True) as retry, \
                patch.object(owner.time, 'sleep'):
            witness = owner.open_project_configuration(app, ui)
        prepare.assert_called_once_with(app, ui)
        retry.assert_called_once_with(app, unittest.mock.ANY, interface_mounting=True)
        self.assertEqual([call.args for call in ui.find.call_args_list],
                         [(43, 'Project configuration'), (43, 'Project configuration')])
        action.doAction.assert_called_once_with(0)
        self.assertEqual(witness['project_configuration']['accessible_chain'], chain)
        self.assertEqual(witness['project_configuration']['entry_lineage'], [WEB, FRAME])
        self.assertEqual(witness['project_configuration']['frame_lineage'], [FRAME, ROOT])
        self.assertTrue(witness['project_configuration']['visible'])
        self.assertEqual(app.observations['project_configuration_navigation'], witness)

    def test_open_project_configuration_never_replays_after_dispatch_failure(self):
        app = SimpleNamespace(observations={'launch_identity': ROOT})
        frame = Mock(get_process_id=Mock(return_value=11))
        node = Mock(get_process_id=Mock(return_value=12))
        action = SimpleNamespace(nActions=1, doAction=Mock(side_effect=RuntimeError('after')))
        node.queryAction.return_value = action
        ui = Mock(api=SimpleNamespace(ROLE_PUSH_BUTTON=43), find=Mock(return_value=node))
        ui.windows.return_value = []
        chain = [{'bus_name': ':1.2', 'path': '/control', 'pid': 12},
                 {'bus_name': ':1.1', 'path': '/frame', 'pid': 11}]
        with patch.object(owner, 'prepare_composer'), \
                patch.object(owner, 'process_identity', side_effect=lambda pid: {11: FRAME, 12: WEB}[pid]), \
                patch.object(owner.accessibility, 'capture', return_value=(frame, chain)), \
                patch.object(owner.lineage, 'capture', side_effect=lambda r, c: [c, r]), \
                patch.object(owner, 'retry_inventory', return_value=True) as retry:
            with self.assertRaises(CaseUnavailable) as refusal:
                owner.open_project_configuration(app, ui)
        retry.assert_not_called()
        action.doAction.assert_called_once_with(0)
        self.assertEqual(app.observations['project_configuration_actions'], [])
        self.assertEqual(refusal.exception.observations['project_configuration_mutation'],
                         {'phase': 'do-action', 'name': 'Project configuration',
                          'role': 43, 'exception_type': 'RuntimeError'})

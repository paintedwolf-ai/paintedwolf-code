"""Normal model selection binds the actual combo/toggle controls to the installed launch."""
from types import SimpleNamespace
from unittest import TestCase
from unittest.mock import Mock, patch

from linux_release.installed_cases import provider_setup as owner
from linux_release.installed_cases.records import CaseUnavailable

ROOT = {'pid': 10, 'parent_pid': 2, 'start_time_ticks': 1}
FRAME = {'pid': 11, 'parent_pid': 10, 'start_time_ticks': 2}
WEB = {'pid': 12, 'parent_pid': 11, 'start_time_ticks': 3}


class ModelPickerTests(TestCase):
    def setUp(self):
        self.app = SimpleNamespace(observations={'launch_identity': ROOT})
        self.frame = Mock()
        self.frame.get_process_id.return_value = 11
        self.nodes = [Mock(), Mock()]
        for node in self.nodes:
            node.get_process_id.return_value = 12
            node.queryAction.return_value.nActions = 1
            node.queryAction.return_value.getName.return_value = 'select'
            node.queryAction.return_value.doAction.return_value = True
        self.ui = Mock(api=SimpleNamespace(ROLE_COMBO_BOX=11, ROLE_TOGGLE_BUTTON=62))
        self.ui.windows.return_value = []
        self.ui.find.side_effect = self.nodes
        self.chain = [{'bus_name': ':1.2', 'path': '/control', 'pid': 12},
                      {'bus_name': ':1.1', 'path': '/frame', 'pid': 11}]
        for target, value in (('process_identity', Mock(side_effect=lambda pid: {11: FRAME, 12: WEB}[pid])),
                              ('accessibility.capture', Mock(return_value=(self.frame, self.chain))),
                              ('lineage.capture', Mock(side_effect=lambda root, child: [child, root]))):
            context = patch('linux_release.installed_cases.provider_setup.' + target, value)
            context.start()
            self.addCleanup(context.stop)

    def test_actual_roles_and_full_provider_model_name_select_once_each(self):
        owner.select_default_model(self.app, self.ui)
        self.assertEqual([call.args for call in self.ui.find.call_args_list],
                         [(11, 'Default model'), (62, 'Installed UI qualification — installed-qualification')])
        for node in self.nodes:
            node.queryAction.return_value.doAction.assert_called_once_with(0)
        records = self.app.observations['provider_model_actions']
        self.assertEqual(len(records), 2)
        self.assertEqual(records[0]['accessible_chain'], self.chain)
        self.assertEqual(records[0]['entry_lineage'], [WEB, FRAME])
        self.assertEqual(records[0]['frame_lineage'], [FRAME, ROOT])
        self.assertTrue(self.app.observations['provider_model_selection']['native_action_completed'])

    def test_wrong_trigger_action_or_multiple_actions_never_activate(self):
        for count, name in ((2, 'select'), (0, 'select'), (1, 'press')):
            with self.subTest(count=count, name=name):
                self.ui.find.side_effect = self.nodes
                action = self.nodes[0].queryAction.return_value
                action.nActions, action.getName.return_value = count, name
                with self.assertRaises(CaseUnavailable):
                    owner.select_default_model(self.app, self.ui)
                action.doAction.assert_not_called()

    def test_replaced_accessible_parent_chain_refuses_before_action(self):
        with patch.object(owner.accessibility, 'capture', side_effect=[(self.frame, self.chain), (self.frame, [])]):
            with self.assertRaisesRegex(ValueError, 'before its action'):
                owner.select_default_model(self.app, self.ui)
        self.nodes[0].queryAction.return_value.doAction.assert_not_called()

    def test_reused_kernel_identity_refuses_before_action(self):
        with patch.object(owner.lineage, 'capture', side_effect=[[WEB, FRAME], [FRAME, ROOT], [WEB, dict(FRAME, start_time_ticks=9)]]):
            with self.assertRaisesRegex(ValueError, 'before its action'):
                owner.select_default_model(self.app, self.ui)
        self.nodes[0].queryAction.return_value.doAction.assert_not_called()

    def test_normal_action_refusal_is_unverified_and_no_success_record(self):
        self.nodes[0].queryAction.return_value.doAction.return_value = False
        with self.assertRaises(CaseUnavailable):
            owner.select_default_model(self.app, self.ui)
        self.assertNotIn('provider_model_selection', self.app.observations)

    def test_changed_launch_after_action_never_certifies_selection(self):
        with patch.object(owner.lineage, 'capture', side_effect=[
                [WEB, FRAME], [FRAME, ROOT], [WEB, FRAME], [FRAME, ROOT], [FRAME, dict(ROOT, start_time_ticks=9)]]):
            with self.assertRaisesRegex(ValueError, 'after its action'):
                owner.select_default_model(self.app, self.ui)
        self.assertNotIn('provider_model_selection', self.app.observations)

    def test_transient_native_accessibility_race_retries_with_same_launch_owner(self):
        class NativeRace(Exception):
            domain, code = 'atspi_error', 0
        self.ui.find.side_effect = [self.nodes[0], self.nodes[0], self.nodes[1], self.nodes[1]]
        action_0 = self.nodes[0].queryAction.return_value
        action_1 = self.nodes[1].queryAction.return_value
        self.nodes[0].queryAction.side_effect = [NativeRace('replaced node'), action_0]
        self.nodes[1].queryAction.side_effect = [NotImplementedError('mounting'), action_1]
        with patch.object(owner, 'retry_inventory', return_value=True) as retry, patch.object(owner.time, 'sleep'):
            owner.select_default_model(self.app, self.ui)
        self.assertEqual(retry.call_count, 2)
        self.assertEqual([call.kwargs for call in retry.call_args_list],
                         [{'interface_mounting': True}, {'interface_mounting': True}])
        action_0.doAction.assert_called_once_with(0)
        action_1.doAction.assert_called_once_with(0)
        self.assertTrue(self.app.observations['provider_model_selection']['native_action_completed'])

    def test_post_dispatch_model_picker_failure_is_never_replayed(self):
        class NativeRace(Exception):
            domain, code = 'atspi_error', 0
        self.nodes[0].queryAction.return_value.doAction.side_effect = NativeRace('after dispatch')
        with patch.object(owner, 'retry_inventory', return_value=True) as retry:
            with self.assertRaises(CaseUnavailable) as refusal:
                owner.select_default_model(self.app, self.ui)
        retry.assert_not_called()
        self.nodes[0].queryAction.return_value.doAction.assert_called_once_with(0)
        self.assertEqual(refusal.exception.observations['model_picker_mutation'],
                         {'phase': 'do-action', 'exception_type': 'NativeRace'})
        self.assertNotIn('provider_model_selection', self.app.observations)

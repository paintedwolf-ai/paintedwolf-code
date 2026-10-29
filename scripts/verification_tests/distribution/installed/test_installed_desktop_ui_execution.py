"""Normal desktop actions and readiness require the captured visible application owner."""
from contextlib import nullcontext
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases import desktop_ui as ui
from linux_release.installed_cases.records import CaseFailed, CaseUnavailable

API = SimpleNamespace(ROLE_FRAME=1, ROLE_DIALOG=2, ROLE_PUSH_BUTTON=3, ROLE_ENTRY=4, ROLE_PASSWORD_TEXT=5,
                      STATE_DEFUNCT=10, STATE_SHOWING=11, STATE_SENSITIVE=12, STATE_ENABLED=13)
IDENTITY = {'pid': 123, 'parent_pid': 100, 'start_time_ticks': 42}


class Node:
    def __init__(self, role, name='', children=(), states=None, pid=123):
        self.role, self.name, self.children, self.pid = role, name, list(children), pid
        self.states = {11, 12, 13} if states is None else states
        self.action = SimpleNamespace(nActions=1, doAction=Mock(return_value=True))
        self.editable = SimpleNamespace(setTextContents=Mock(return_value=True))
    @property
    def childCount(self):
        return len(self.children)
    def __getitem__(self, index):
        return self.children[index]
    def getRole(self):
        return self.role
    def getState(self):
        return SimpleNamespace(contains=lambda state: state in self.states)
    def get_process_id(self):
        return self.pid
    def queryAction(self):
        return self.action
    def queryEditableText(self):
        return self.editable


def surface(children):
    window = Node(API.ROLE_FRAME, children=children)
    desktop = Node(0, children=[Node(0, children=[window])])
    return SimpleNamespace(**vars(API), Registry=SimpleNamespace(getDesktop=lambda index: desktop)), window


class DesktopUIOwnershipTests(unittest.TestCase):
    def test_interactive_retry_requires_same_owned_identity_and_one_deadline(self):
        api, _ = surface([Node(API.ROLE_PUSH_BUTTON, 'Send')])
        witness = {'normal_actionable_control': True}
        with patch.object(ui, 'load_api', return_value=api), patch.object(ui.time, 'sleep'), \
                patch.object(ui.DesktopUI, 'interactive', side_effect=[IndexError(), witness]) as observe:
            self.assertEqual(ui.interactive_window(self.app), witness)
        self.assertEqual(observe.call_count, 2)
        self.assertEqual(observe.call_args_list[0].args, observe.call_args_list[1].args)
        with patch.object(ui, 'load_api', return_value=api), \
                patch.object(ui.DesktopUI, 'interactive', side_effect=IndexError()) as observe, \
                patch.object(ui, 'process_identity', return_value={**IDENTITY, 'start_time_ticks': 43}):
            with self.assertRaises(CaseFailed):
                ui.interactive_window(self.app)
        observe.assert_called_once()

    def test_negative_child_count_invalidates_whole_inventory_without_actions(self):
        class Invalidated(Node):
            @property
            def childCount(self):
                return -1
        node = Invalidated(API.ROLE_FRAME)
        actor = ui.DesktopUI(self.app, API)
        with self.assertRaises(ui._InventoryChanged):
            list(actor.descendants(node))
        node.action.doAction.assert_not_called()

    def test_observer_bound_retains_exact_single_read_facts_without_authorizing_action(self):
        class Oversized(Node):
            reads = 0
            @property
            def childCount(self):
                self.reads += 1
                return 1025
        oversized = Oversized(API.ROLE_FRAME)
        actor = ui.DesktopUI(self.app, API)
        with self.assertRaises(CaseUnavailable) as refusal:
            list(actor.descendants(oversized))
        self.assertEqual(oversized.reads, 1)
        self.assertEqual(refusal.exception.observations['desktop_inventory_bound'], {
            'visited': 1, 'depth': 0, 'child_count': 1025,
            'limits': {'visited': 4096, 'depth': 64, 'child_count': 1024}})
        oversized.action.doAction.assert_not_called()

    def setUp(self):
        self.app = SimpleNamespace(observations={'launch_identity': IDENTITY, 'session': {}}, process=SimpleNamespace(poll=lambda: None))
        identity = patch.object(ui, 'process_identity', return_value=IDENTITY)
        identity.start()
        self.addCleanup(identity.stop)
        bus = patch.object(ui, 'accessibility_bus', return_value=nullcontext())
        bus.start()
        self.addCleanup(bus.stop)

    def test_actual_named_buttons_and_editable_inputs_use_only_normal_mutation_interfaces(self):
        button, entry = Node(API.ROLE_PUSH_BUTTON, 'Send'), Node(API.ROLE_ENTRY, 'Message')
        api, _ = surface([button, entry])
        with ui.DesktopUI(self.app, api) as actor:
            actor.fill(API.ROLE_ENTRY, 'Message', 'fixed fixture')
            actor.click('Send')
            witness = actor.interactive()
        entry.editable.setTextContents.assert_called_once_with('fixed fixture')
        button.action.doAction.assert_called_once_with(0)
        self.assertEqual(witness['ui_identity'], IDENTITY)
        self.assertNotIn('fixed fixture', repr(witness))
        self.assertNotIn('Message', repr(witness))

    def test_defunct_insensitive_and_decoration_only_windows_are_not_interactive(self):
        for states in ({10, 11, 12, 13}, {11, 13}):
            api, window = surface([Node(API.ROLE_PUSH_BUTTON, 'Send')])
            window.states = states
            with ui.DesktopUI(self.app, api) as actor:
                self.assertIsNone(actor.interactive())
        api, _ = surface([Node(API.ROLE_PUSH_BUTTON, 'Close')])
        with ui.DesktopUI(self.app, api) as actor:
            self.assertIsNone(actor.interactive())

    def test_foreign_reused_or_ambiguous_control_is_never_activated(self):
        button = Node(API.ROLE_PUSH_BUTTON, 'Send')
        api, _ = surface([button, button])
        with ui.DesktopUI(self.app, api) as actor, self.assertRaises(CaseUnavailable) as refusal:
            actor.click('Send')
        self.assertEqual(refusal.exception.observations['desktop_control'],
                         {'role': API.ROLE_PUSH_BUTTON, 'name': 'Send', 'match_count': 2, 'observed_roles': [API.ROLE_PUSH_BUTTON]})
        api, _ = surface([button])
        with patch.object(ui, 'process_identity', return_value={**IDENTITY, 'start_time_ticks': 43}), \
                patch.object(ui.time, 'monotonic', side_effect=iter(range(100))), patch.object(ui.time, 'sleep'):
            with ui.DesktopUI(self.app, api) as actor, self.assertRaises(CaseUnavailable):
                actor.click('Send')
        button.action.doAction.assert_not_called()

    def test_control_waits_for_actual_async_render_without_activating_missing_node(self):
        button = Node(API.ROLE_PUSH_BUTTON, 'Send')
        missing, _ = surface([])
        ready, _ = surface([button])
        api = SimpleNamespace(**vars(API), Registry=SimpleNamespace(
            getDesktop=Mock(side_effect=[missing.Registry.getDesktop(0), ready.Registry.getDesktop(0)])))
        with ui.DesktopUI(self.app, api) as actor, patch.object(ui.time, 'sleep'):
            actor.click('Send')
        self.assertEqual(api.Registry.getDesktop.call_count, 2)
        button.action.doAction.assert_called_once_with(0)

    def test_incomplete_inventory_retains_exact_requested_control_without_action(self):
        button = Node(API.ROLE_PUSH_BUTTON, 'Send')
        api, _ = surface([button])
        with ui.DesktopUI(self.app, api) as actor:
            with patch.object(actor, 'descendants', side_effect=CaseUnavailable('normal-native-control-observation-deadline')):
                with self.assertRaises(CaseUnavailable) as refusal:
                    actor.click('Send')
        self.assertEqual(refusal.exception.observations['desktop_control'], {
            'role': API.ROLE_PUSH_BUTTON, 'name': 'Send', 'match_count': 0,
            'inventory_complete': False, 'observed_roles': []})
        button.action.doAction.assert_not_called()

    def test_typed_native_inventory_race_retries_but_unknown_error_is_retained(self):
        class NativeRace(Exception):
            domain, code = 'atspi_error', 0
        button = Node(API.ROLE_PUSH_BUTTON, 'Send')
        api, window = surface([button])
        self.app.observations = {'launch_identity': IDENTITY, 'session': {}}
        with ui.DesktopUI(self.app, api) as actor, patch.object(ui.time, 'sleep'):
            with patch.object(actor, 'descendants', side_effect=[NativeRace('not retained'), iter([button])]):
                actor.click('Send')
        button.action.doAction.assert_called_once_with(0)
        self.assertEqual(self.app.observations['desktop_inventory_retry']['domain'], 'atspi_error')
        with ui.DesktopUI(self.app, api) as actor:
            with patch.object(actor, 'descendants', side_effect=RuntimeError('not retained')):
                with self.assertRaises(CaseUnavailable) as refusal:
                    actor.find(API.ROLE_PUSH_BUTTON, 'Send')
        self.assertNotIn('not retained', str(refusal.exception))

    def test_toolkit_exception_cannot_copy_input_into_retained_error(self):
        entry = Node(API.ROLE_ENTRY, 'Message')
        entry.editable.setTextContents.side_effect = RuntimeError('do not retain fixture content')
        api, _ = surface([entry])
        with ui.DesktopUI(self.app, api) as actor:
            with self.assertRaises(CaseUnavailable) as error:
                actor.fill(API.ROLE_ENTRY, 'Message', 'private fixture content')
        self.assertNotIn('fixture', str(error.exception))

    def test_accessibility_tree_actions_have_one_shared_wall_clock_bound(self):
        api, window = surface([Node(API.ROLE_PUSH_BUTTON, 'Send')])
        with ui.DesktopUI(self.app, api) as desktop:
            with patch.object(ui.time, 'monotonic', side_effect=[0, 16]):
                with self.assertRaises(CaseUnavailable):
                    desktop.find(API.ROLE_PUSH_BUTTON, 'Send')
            with patch.object(ui.time, 'monotonic', side_effect=[0, 16]):
                with self.assertRaises(CaseUnavailable):
                    list(desktop.descendants(window))

    def test_observed_product_crash_is_failed_instead_of_missing_ui_fixture(self):
        api, _ = surface([])
        self.app.process = SimpleNamespace(poll=lambda: -6)
        with patch.object(ui, 'load_api', return_value=api):
            with self.assertRaises(CaseFailed) as failure:
                ui.interactive_window(self.app)
            self.assertNotIsInstance(failure.exception, CaseUnavailable)
            self.assertEqual(failure.exception.observations["launcher_exit_code"], -6)
        self.app.process = SimpleNamespace(poll=lambda: 0)
        with patch.object(ui, 'load_api', return_value=api):
            with self.assertRaises(CaseFailed) as failure:
                ui.interactive_window(self.app)
            self.assertNotIsInstance(failure.exception, CaseUnavailable)

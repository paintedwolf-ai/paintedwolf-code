"""Normal desktop actions and readiness require the captured visible application owner."""
from contextlib import nullcontext
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases import desktop_ui as ui
from linux_release.installed_cases.records import CaseUnavailable

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
        with ui.DesktopUI(self.app, api) as actor, self.assertRaises(CaseUnavailable):
            actor.click('Send')
        api, _ = surface([button])
        with patch.object(ui, 'process_identity', return_value={**IDENTITY, 'start_time_ticks': 43}):
            with ui.DesktopUI(self.app, api) as actor, self.assertRaises(CaseUnavailable):
                actor.click('Send')
        button.action.doAction.assert_not_called()

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
            with self.assertRaises(ValueError) as failure:
                ui.interactive_window(self.app)
            self.assertNotIsInstance(failure.exception, CaseUnavailable)
        self.app.process = SimpleNamespace(poll=lambda: 0)
        with patch.object(ui, 'load_api', return_value=api):
            with self.assertRaises(ValueError) as failure:
                ui.interactive_window(self.app)
            self.assertNotIsInstance(failure.exception, CaseUnavailable)

"""Fresh password typing has only fixed named setup fields and an owned native modal."""
import copy
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases.records import CaseFailed, CaseUnavailable
from linux_release.installed_input import password_focus


class PasswordFocusTests(unittest.TestCase):
    def setUp(self):
        self.identity = {'pid': 123, 'parent_pid': 1, 'start_time_ticks': 42}
        self.main = {'bus_name': ':1.37', 'object_path': '/org/a11y/atspi/accessible/1',
                     'process': self.identity, 'lineage': [self.identity]}
        self.dialog = {**self.main, 'object_path': '/org/a11y/atspi/accessible/2'}
        self.password = {**self.main, 'object_path': '/org/a11y/atspi/accessible/3'}
        self.proof = {'launch': self.identity, 'engine': {'pid': 124, 'parent_pid': 123, 'start_time_ticks': 43},
            'main': self.main, 'control': None, 'surfaces': [self.row(self.main, active=False),
                self.row(self.dialog, kind='presence-dialog', modal=True, parent=self.main)]}
        self.app = Mock()
        self.ui = Mock(api=SimpleNamespace(ROLE_PASSWORD_TEXT=1, STATE_FOCUSED=2))
        self.observer = Mock(app=self.app, ui=self.ui)
        self.observer.observe.return_value = self.proof
        self.observer._key.return_value = ('dialog',)
        self.observer.nodes = {('dialog',): (Mock(), {})}
        self.observer._identity.return_value = self.password
        self.control = Mock()
        self.control.getRole.return_value = 1
        self.control.getState().contains.return_value = True
        self.ui.visible.return_value = True
        self.ui.descendants.return_value = [self.control]

    def row(self, identity, **changes):
        return {'identity': identity, 'kind': 'frame', 'visible': True, 'active': True,
                'defunct': False, 'modal': False, 'parent': None, **changes}

    def test_unnamed_password_requires_unique_visible_focused_native_control(self):
        with patch.object(password_focus, 'EntryFocus') as entry:
            focus = password_focus.FixtureFocus(self.app, self.ui, 'native-presence-password', self.observer)
            self.assertEqual(focus.observe()['password']['identity'], self.password)
            entry.assert_not_called()
        for controls in ([], [self.control, self.control]):
            self.ui.descendants.return_value = controls
            with self.assertRaises(CaseUnavailable):
                password_focus.FixtureFocus(self.app, self.ui, 'native-presence-password', self.observer)
        self.ui.descendants.return_value = [self.control]
        self.control.getState().contains.return_value = False
        with self.assertRaises(CaseUnavailable):
            password_focus.FixtureFocus(self.app, self.ui, 'native-presence-password', self.observer)

    def test_replacement_control_or_parent_relation_is_not_adopted(self):
        focus = password_focus.FixtureFocus(self.app, self.ui, 'native-presence-password', self.observer)
        self.observer._identity.return_value = {**self.password, 'object_path': '/replacement'}
        with self.assertRaises(CaseFailed):
            focus.observe()
        self.observer._identity.return_value = self.password
        changed = copy.deepcopy(self.proof)
        changed['surfaces'][1]['parent'] = None
        self.observer.observe.return_value = changed
        with self.assertRaises(ValueError):
            focus.observe()

    def test_setup_uses_fixed_named_password_entry_and_rejects_generic_stage(self):
        identity = self.identity
        proof = {'launch': identity, 'entry': identity, 'frame': identity,
            'frame_lineage': [identity], 'entry_lineage': [identity],
            'accessible_chain': [{'bus_name': ':1.2', 'path': '/frame', 'pid': 123}],
            'entry_focused': True, 'frame_active': True, 'frame_visible': True}
        with patch.object(password_focus, 'EntryFocus') as entry:
            entry.return_value.observe.return_value = proof
            password_focus.FixtureFocus(self.app, self.ui, 'new-app-password')
            entry.assert_called_once_with(self.app, self.ui, 'New app password', password=True)
        with self.assertRaises(ValueError):
            password_focus.FixtureFocus(self.app, self.ui, 'any-entry')
        with self.assertRaises(ValueError):
            password_focus.FixtureFocus(self.app, self.ui, 'native-presence-password')

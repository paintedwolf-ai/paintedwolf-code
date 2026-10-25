"""An active native agent transaction still requires one actual modal/password surface."""
import copy
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release.installed_input import polkit_target
from scripts.linux_release.installed_cases.records import CaseUnavailable
from verification_tests.distribution.installed.polkit.test_installed_polkit_stage_execution import target


class Node:
    def __init__(self, path, role, states, parent=None, children=(), pid=200):
        self.app = SimpleNamespace(bus_name=':1.90')
        self.path, self.role, self.states = path, role, set(states)
        self.parent, self.children, self.pid = parent, list(children), pid

    @property
    def childCount(self):
        return len(self.children)

    def __getitem__(self, index):
        return self.children[index]

    def getState(self):
        return SimpleNamespace(contains=lambda key: key in self.states)

    def getRole(self):
        return self.role

    def get_process_id(self):
        return self.pid

    def get_parent(self):
        return self.parent


class PolkitTargetTests(unittest.TestCase):
    def fixture(self):
        api = SimpleNamespace(ROLE_WINDOW=1, ROLE_PASSWORD_TEXT=2, ROLE_DIALOG=10,
            STATE_DEFUNCT=3, STATE_SHOWING=4, STATE_VISIBLE=5, STATE_ENABLED=6,
            STATE_SENSITIVE=7, STATE_ACTIVE=8, STATE_FOCUSED=9, STATE_MODAL=11)
        window = Node('/window', 1, {4, 5, 6, 7, 8})
        modal = Node('/modal', 10, {4, 5, 6, 7, 11}, window)
        password = Node('/password', 2, {4, 5, 6, 7, 9}, modal)
        modal.children, window.children = [password], [modal]
        application = Node('/application', 0, set(), children=[window])
        desktop = Node('/desktop', 0, set(), children=[application], pid=100)
        api.Registry = SimpleNamespace(getDesktop=lambda _: desktop)
        observer = polkit_target.Target.__new__(polkit_target.Target)
        observer.api, observer.entered, observer.original = api, True, None
        observer.native = Mock(observe=lambda **kwargs: copy.deepcopy(target()['session']))
        return observer, window, modal, password

    def test_exact_modal_ancestor_is_captured_without_names_or_value_readback(self):
        observer, _, _, _ = self.fixture()
        result = observer.observe(target()['pending'])
        self.assertEqual([row['path'] for row in result['ancestors']], ['/password', '/modal', '/window'])
        self.assertEqual(result['modal']['path'], '/modal')
        self.assertNotIn('name', result)
        self.assertNotIn('text', result)

    def test_any_same_shell_second_modal_window_or_password_is_ambiguous(self):
        for kind in ('modal', 'window', 'password'):
            observer, window, modal, password = self.fixture()
            if kind == 'modal':
                window.children.append(Node('/othermodal', 10, modal.states, window))
            elif kind == 'window':
                window.children.append(Node('/otherwindow', 1, window.states, window))
            else:
                modal.children.append(Node('/otherpassword', 2, password.states, modal))
            with self.subTest(kind=kind), self.assertRaises(CaseUnavailable):
                observer.observe(target()['pending'])

    def test_modal_role_state_parent_and_password_focus_are_required(self):
        for kind in ('role', 'modal', 'parent', 'focus', 'pid'):
            observer, window, modal, password = self.fixture()
            if kind == 'role':
                modal.role = 0
            elif kind == 'modal':
                modal.states.remove(11)
            elif kind == 'parent':
                password.parent = window
            elif kind == 'pid':
                password.pid += 1
            else:
                password.states.remove(9)
            with self.subTest(kind=kind), self.assertRaises((ValueError, CaseUnavailable)):
                observer.observe(target()['pending'])

    def test_new_accessible_after_initial_target_cannot_continue_same_input(self):
        observer, _, _, password = self.fixture()
        observer.observe(target()['pending'])
        password.path += '_replaced'
        with self.assertRaises(ValueError):
            observer.observe(target()['pending'])

    def test_session_owner_replacement_during_target_capture_refuses(self):
        observer, _, _, _ = self.fixture()
        before = target()['session']
        after = copy.deepcopy(before)
        after['authorities']['shell']['start_time_ticks'] += 1
        after['authorities']['locker']['process']['start_time_ticks'] += 1
        observer.native.observe = Mock(side_effect=[before, after])
        with self.assertRaises(ValueError):
            observer.observe(target()['pending'])

    def test_native_rpc_and_inventory_share_one_success_deadline(self):
        observer, _, _, _ = self.fixture()
        calls = []
        def native(**kwargs):
            calls.append(kwargs['deadline'])
            return copy.deepcopy(target()['session'])
        observer.native.observe = native
        with patch.object(polkit_target.time, 'monotonic', return_value=10):
            observer.observe(target()['pending'])
        self.assertEqual(calls, [15, 15, 15])
        observer, _, _, _ = self.fixture()
        observer.clock = 10
        calls = []
        def slow_native(**kwargs):
            calls.append(kwargs['deadline'])
            if len(calls) == 3:
                observer.clock = 16
            return copy.deepcopy(target()['session'])
        observer.native.observe = slow_native
        with patch.object(polkit_target.time, 'monotonic', side_effect=lambda: observer.clock):
            with self.assertRaises(CaseUnavailable):
                observer.observe(target()['pending'])


if __name__ == '__main__':
    unittest.main()

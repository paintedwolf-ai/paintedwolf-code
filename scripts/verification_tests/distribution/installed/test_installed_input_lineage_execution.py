"""Native focus pins the actual UI frame separately from its bootstrap authority."""
import copy
import os
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_input import lineage, protocol
from linux_release.installed_input import focus as focus_owner


class InputLineageTests(unittest.TestCase):
    def setUp(self):
        self.root = {'pid': 100, 'parent_pid': 50, 'start_time_ticks': 42}
        self.stage = {'pid': 101, 'parent_pid': 100, 'start_time_ticks': 43}
        self.frame = {'pid': 102, 'parent_pid': 101, 'start_time_ticks': 44}
        self.chain = [self.frame, self.stage, self.root]

    def test_capture_rechecks_every_kernel_birth_and_parent_fact(self):
        facts = {row['pid']: row for row in self.chain}
        with patch.object(lineage, 'process_identity', side_effect=facts.__getitem__) as observe:
            self.assertEqual(lineage.capture(self.root, self.frame), self.chain)
        self.assertEqual([call.args[0] for call in observe.call_args_list], [102, 101, 100, 100, 101, 102])
        value = {'launch': self.root, 'entry': self.frame, 'frame': self.frame,
                 'frame_lineage': self.chain, 'entry_lineage': [self.frame],
                 'accessible_chain': [{'bus_name': ':1.2', 'path': '/frame', 'pid': 102}], 'entry_focused': True, 'frame_active': True, 'frame_visible': True}
        self.assertIs(protocol.focus(value), value)

    def test_wrong_root_cycle_missing_link_and_excess_depth_are_refused(self):
        variants = [[], [self.frame, self.root], [*self.chain, self.frame],
                    [self.frame] * (lineage.MAX_ANCESTRY + 1)]
        for chain in variants:
            with self.subTest(chain=chain), self.assertRaises(ValueError):
                lineage.validate(chain, self.root, self.frame)
        changed = copy.deepcopy(self.chain)
        changed[-1]['start_time_ticks'] += 1
        with self.assertRaises(ValueError):
            lineage.validate(changed, self.root, self.frame)
        for rows in ([self.frame, self.stage, {**self.root, 'parent_pid': 101}],
                     [self.frame, {**self.stage, 'parent_pid': 102}]):
            with patch.object(lineage, 'process_identity', side_effect=rows), self.assertRaises(ValueError):
                lineage.capture({**self.root, 'pid': 999}, self.frame)

    def test_pid_reuse_reparenting_and_disappeared_link_never_authorize_input(self):
        for changed in ({**self.root, 'start_time_ticks': 99}, {**self.root, 'parent_pid': 9}):
            with patch.object(lineage, 'process_identity', side_effect=[*self.chain, changed]), self.assertRaises(ValueError):
                lineage.capture(self.root, self.frame)
        with patch.object(lineage, 'process_identity', side_effect=[self.frame, FileNotFoundError()]), self.assertRaises(OSError):
            lineage.capture(self.root, self.frame)
        with patch.object(lineage, 'process_identity', return_value=self.root), self.assertRaises(ValueError):
            lineage.capture(self.root, self.frame)
        for value in ({**self.root, 'pid': True}, {**self.root, 'start_time_ticks': 0}, {'pid': 100}):
            with self.assertRaises(ValueError):
                lineage.identity(value)

    def test_focused_frame_stays_pinned_when_another_owned_child_appears(self):
        ui, node, frame = MagicMock(), MagicMock(), MagicMock()
        ui.api.ROLE_FRAME, ui.api.ROLE_DIALOG = 23, 16
        ui.find.return_value = node
        node.get_process_id.return_value = self.frame['pid']
        node.queryComponent.return_value.grabFocus.return_value = True
        node.getState.return_value.contains.return_value = True
        node.app.bus_name, node.path = ':1.3', '/entry'
        frame.app.bus_name, frame.path = ':1.2', '/frame'
        node.get_parent.return_value = frame
        ui.windows.side_effect = lambda: iter([(self.frame, frame)])
        frame.getRole.return_value = 23
        frame.get_process_id.return_value = self.frame['pid']
        frame.getState.return_value.contains.return_value = True
        app = SimpleNamespace(observations={'launch_identity': self.root}, require_running=lambda: None)
        peer = {**self.frame, 'pid': 103, 'start_time_ticks': 45}
        facts = {row['pid']: row for row in [*self.chain, peer]}
        with patch.object(focus_owner, 'process_identity', side_effect=facts.__getitem__), \
                patch.object(lineage, 'process_identity', side_effect=facts.__getitem__), \
                patch.object(focus_owner, 'owned_process', return_value=True):
            actor = focus_owner.EntryFocus(app, ui, 'Fixed entry')
            observed = actor.observe()
            self.assertEqual(observed['frame'], self.frame)
            self.assertNotEqual(observed['frame'], observed['launch'])
            frame.get_process_id.return_value = peer['pid']
            with self.assertRaises(focus_owner.CaseFailed):
                actor.observe()


class RefusalWindowDiagnosticsTests(unittest.TestCase):
    def fixture(self, count=2):
        actor = focus_owner.EntryFocus.__new__(focus_owner.EntryFocus)
        actor.app = SimpleNamespace(observations={'session': {'User': str(os.getuid())}})
        actor.ui = MagicMock()
        actor.ui.api.ROLE_FRAME, actor.ui.api.ROLE_DIALOG, actor.ui.api.STATE_ACTIVE = 23, 16, 1
        windows, applications = [], []
        for index in range(count):
            pid = 102 + index
            window = SimpleNamespace(app=SimpleNamespace(bus_name=f':1.{index + 2}'), path=f'/window{index}',
                get_process_id=Mock(return_value=pid), getRole=Mock(return_value=23),
                getState=Mock(return_value=SimpleNamespace(contains=lambda value: True)))
            application = MagicMock()
            application.get_process_id.return_value, application.childCount = pid, 1
            application.__getitem__.return_value = window
            applications.append(application)
            windows.append(window)
        desktop = MagicMock()
        desktop.childCount = count
        desktop.__getitem__.side_effect = applications.__getitem__
        actor.ui.api.Registry.getDesktop.return_value = desktop
        actor.ui.visible.return_value = True
        return actor, applications, windows

    def test_same_uid_external_top_level_is_identified_without_names_text_or_input(self):
        actor, applications, windows = self.fixture()
        owner = lambda pid, uid: {'pid': pid, 'parent_pid': 1, 'start_time_ticks': pid, 'uid': uid}
        with patch.object(focus_owner.Path, 'stat', return_value=SimpleNamespace(st_uid=os.getuid())), \
                patch.object(focus_owner, 'kernel_owner', side_effect=owner), \
                patch.object(focus_owner, 'owned_process', side_effect=lambda pid, app: pid == 102), \
                patch.object(focus_owner.time, 'monotonic', return_value=0):
            result = actor._diagnose_windows()
        self.assertTrue(result['complete'])
        self.assertEqual([row['app_owned'] for row in result['windows']], [True, False])
        self.assertEqual([row['accessible']['path'] for row in result['windows']], ['/window0', '/window1'])
        self.assertEqual(set(result['windows'][0]), {'process', 'accessible', 'role', 'active', 'visible', 'app_owned'})
        self.assertFalse(any(hasattr(window, key) for window in windows for key in ('name', 'queryText', 'queryComponent')))
        actor.ui.find.assert_not_called()

    def test_other_uid_is_not_traversed_and_count_and_time_bounds_are_explicit(self):
        actor, applications, windows = self.fixture()
        with patch.object(focus_owner.Path, 'stat', return_value=SimpleNamespace(st_uid=os.getuid() + 1)), \
                patch.object(focus_owner.time, 'monotonic', return_value=0):
            result = actor._diagnose_windows()
        self.assertTrue(result['complete'])
        self.assertEqual(result['windows'], [])
        for application in applications: application.__getitem__.assert_not_called()
        for window in windows: window.getState.assert_not_called()
        actor, _, _ = self.fixture(count=33)
        with patch.object(focus_owner.Path, 'stat', return_value=SimpleNamespace(st_uid=os.getuid() + 1)), \
                patch.object(focus_owner.time, 'monotonic', return_value=0):
            result = actor._diagnose_windows()
        self.assertFalse(result['complete'])
        self.assertEqual(actor.ui.api.Registry.getDesktop.return_value.__getitem__.call_count, 32)
        with patch.object(focus_owner.time, 'monotonic', side_effect=[0, 0, 0.3]):
            result = actor._diagnose_windows()
        self.assertFalse(result['complete'])
        self.assertEqual(result['failure_type'], 'TimeoutError')

    def test_window_limit_and_owner_replacement_keep_the_snapshot_incomplete(self):
        actor, _, _ = self.fixture(count=17)
        owner = lambda pid, uid: {'pid': pid, 'parent_pid': 1, 'start_time_ticks': pid, 'uid': uid}
        with patch.object(focus_owner.Path, 'stat', return_value=SimpleNamespace(st_uid=os.getuid())), \
                patch.object(focus_owner, 'kernel_owner', side_effect=owner), \
                patch.object(focus_owner, 'owned_process', return_value=False), \
                patch.object(focus_owner.time, 'monotonic', return_value=0):
            result = actor._diagnose_windows()
        self.assertFalse(result['complete'])
        self.assertEqual(result['windows_examined'], 16)
        self.assertEqual(len(result['windows']), 16)
        first, replaced = owner(102, os.getuid()), {**owner(102, os.getuid()), 'start_time_ticks': 999}
        actor, _, _ = self.fixture(count=1)
        with patch.object(focus_owner.Path, 'stat', return_value=SimpleNamespace(st_uid=os.getuid())), \
                patch.object(focus_owner, 'kernel_owner', side_effect=[first, replaced]), \
                patch.object(focus_owner, 'owned_process', return_value=False), \
                patch.object(focus_owner.time, 'monotonic', return_value=0):
            result = actor._diagnose_windows()
        self.assertFalse(result['complete'])
        self.assertEqual(result['failure_type'], 'ValueError')
        self.assertEqual(result['windows'], [])

    def test_terminal_diagnostic_failure_preserves_original_focus_exception_and_retry_path(self):
        actor, _, _ = self.fixture()
        actor.deadline = 90
        original = focus_owner._FocusUnavailable('original refusal')
        original.observations = {'editor_focus': {'container_active': False}}
        with patch.object(actor, '_observe_once', side_effect=original) as observe, \
                patch.object(actor, '_diagnose_windows', side_effect=RuntimeError('private diagnostic prose')) as diagnostic, \
                patch.object(focus_owner.time, 'monotonic', side_effect=[0, 1, 3]), \
                patch.object(focus_owner.time, 'sleep'):
            with self.assertRaises(focus_owner._FocusUnavailable) as refusal: actor.observe()
        self.assertIs(refusal.exception, original)
        self.assertEqual(observe.call_count, 2)
        diagnostic.assert_called_once_with()
        self.assertEqual(original.observations['editor_focus'], {'container_active': False})
        self.assertEqual(original.observations['focus_diagnostics'], {'complete': False, 'failure_type': 'RuntimeError'})
        self.assertNotIn('private diagnostic prose', str(original.observations))

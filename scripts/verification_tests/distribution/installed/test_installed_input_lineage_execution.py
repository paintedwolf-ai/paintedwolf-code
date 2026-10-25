"""Native focus pins the actual UI frame separately from its bootstrap authority."""
import copy
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

    def test_entry_lookup_cannot_authorize_focus_after_its_absolute_deadline(self):
        ui, node = MagicMock(), MagicMock()
        ui.find.return_value = node
        node.get_process_id.return_value = self.frame['pid']
        app = SimpleNamespace(observations={'launch_identity': self.root})
        with patch.object(focus_owner.time, 'monotonic', side_effect=[0, 0, 10]), \
                patch.object(focus_owner, 'process_identity', return_value=self.frame):
            with self.assertRaises(focus_owner.CaseUnavailable):
                focus_owner.EntryFocus(app, ui, 'Crossbar…', deadline=10)
        ui.find.assert_called_once_with(ui.api.ROLE_ENTRY, 'Crossbar…', deadline=10)
        node.queryComponent.assert_not_called()

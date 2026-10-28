"""Native frame observations retain identity through asynchronous teardown."""
from contextlib import ExitStack
import unittest
from unittest.mock import Mock

from linux_release.installed_cases.records import CaseUnavailable
from linux_release.installed_input import surface_wire


class FrameObserverTests(unittest.TestCase):
    def setUp(self):
        from types import SimpleNamespace
        from unittest.mock import patch
        from linux_release.installed_input import surface
        from linux_release.installed_cases import desktop_ui
        self.surface = surface
        self.context = ExitStack()
        self.addCleanup(self.context.close)
        self.identity = {'pid': 123, 'parent_pid': 1, 'start_time_ticks': 42}
        self.engine = {'pid': 124, 'parent_pid': 123, 'start_time_ticks': 43}
        api = SimpleNamespace(ROLE_FRAME=1, ROLE_DIALOG=2, ROLE_PUSH_BUTTON=3,
            STATE_ACTIVE=4, STATE_DEFUNCT=5, STATE_MODAL=6, STATE_FOCUSED=7,
            RELATION_SUBWINDOW_OF=8)
        self.ui = Mock(api=api)
        self.app = Mock(observations={'launch_identity': self.identity, 'engine_identity': self.engine})
        self.active, self.defunct = True, False
        self.main = self.node('/org/a11y/atspi/accessible/1', api.ROLE_FRAME)
        self.main.getState().contains.side_effect = lambda state: (
            self.active if state == api.STATE_ACTIVE else self.defunct if state == api.STATE_DEFUNCT else False)
        self.visible = [self.main]
        self.ui.windows.side_effect = lambda deadline=None: [(self.identity, node) for node in self.visible]
        self.ui.visible.return_value = True
        self.ui.descendants.return_value = []
        def identity(pid):
            return self.identity.copy() if pid == 123 else self.engine.copy()
        self.identities = self.context.enter_context(patch.object(surface, 'process_identity', side_effect=identity))
        self.context.enter_context(patch.object(desktop_ui, 'process_identity', side_effect=identity))
        self.context.enter_context(patch.object(surface.lineage, 'process_identity', side_effect=identity))
        self.context.enter_context(patch.object(surface.os, 'pidfd_open', side_effect=[101, 102, 103], create=True))
        self.close_fd = self.context.enter_context(patch.object(surface.os, 'close'))
        self.observer = surface.FrameObserver(self.app, self.ui)
        self.addCleanup(self.observer.close)

    def node(self, path, role, *, name='', modal=False):
        from types import SimpleNamespace
        node = Mock(app=SimpleNamespace(bus_name=':1.37'), path=path)
        node.name = name
        node.get_process_id.return_value = 123
        node.getRole.return_value = role
        node.getRelationSet.return_value = []
        node.getState().contains.side_effect = lambda state: state == self.ui.api.STATE_MODAL and modal
        return node

    def test_real_retained_object_becomes_hidden_without_substituting_peer(self):
        initial = self.observer.observe()
        peer = self.node('/org/a11y/atspi/accessible/2', self.ui.api.ROLE_FRAME)
        peer.getState().contains.side_effect = lambda state: state == self.ui.api.STATE_ACTIVE
        self.active = False
        self.visible = [peer]
        self.ui.visible.side_effect = lambda node: node is peer
        after = self.observer.observe()
        self.assertEqual(after['main'], initial['main'])
        self.assertFalse(after['surfaces'][0]['visible'])
        self.assertFalse(after['surfaces'][0]['defunct'])
        self.assertTrue(after['surfaces'][1]['active'])
        self.main.path = '/org/a11y/atspi/accessible/9'
        with self.assertRaisesRegex(ValueError, 'address changed'):
            self.observer.observe()

    def test_native_dialog_parent_is_observed_and_defunct_dialog_retains_that_capture(self):
        dialog = self.node('/org/a11y/atspi/accessible/3', self.ui.api.ROLE_DIALOG,
                           name='Quit Painted Wolf Code?', modal=True)
        self.visible = [self.main, dialog]
        with self.assertRaisesRegex(ValueError, 'observed-original-frame-parent'):
            self.observer.observe()
        relation = Mock()
        relation.getRelationType.return_value = self.ui.api.RELATION_SUBWINDOW_OF
        relation.getNTargets.return_value = 1
        relation.getTarget.return_value = self.main
        dialog.getRelationSet.return_value = [relation]
        original = self.observer.observe()['surfaces'][1]
        self.visible = [self.main]
        dialog.getState().contains.side_effect = lambda state: state == self.ui.api.STATE_DEFUNCT
        dialog.get_process_id.return_value = 0
        closed = self.observer.observe()['surfaces'][1]
        self.assertEqual(closed['identity'], original['identity'])
        self.assertEqual(closed['parent'], original['parent'])
        self.assertTrue(closed['defunct'])
        self.assertFalse(closed['visible'])

    def test_runtime_replacement_refuses_input_and_quit_needs_both_pinned_exits(self):
        before = self.observer.observe()
        self.identities.side_effect = lambda pid: {**self.identity, 'start_time_ticks': 99}
        with self.assertRaisesRegex(ValueError, 'identity changed'):
            self.observer.observe()
        from unittest.mock import patch
        poll = Mock()
        poll.poll.return_value = [(101, 1)]
        self.app.process.poll.return_value = 0
        with patch.object(self.surface.select, 'poll', return_value=poll):
            after = self.observer.settle('workspace-approve-quit-v1', before)
        self.assertEqual(after['exited'], {'launch': True, 'engine': True, 'frame': True})
        self.assertEqual(after['exit_code'], 0)

    def test_distinct_native_frame_binds_exactly_to_bootstrap_and_refuses_reparenting(self):
        from unittest.mock import patch
        frame = {'pid': 125, 'parent_pid': 123, 'start_time_ticks': 44}
        mapping = {123: self.identity, 124: self.engine, 125: frame}
        observe = lambda pid: mapping[pid].copy()
        self.identities.side_effect = observe
        native = self.node('/org/a11y/atspi/accessible/9', self.ui.api.ROLE_FRAME)
        native.get_process_id.return_value = 125
        native.getState().contains.side_effect = lambda state: state == self.ui.api.STATE_ACTIVE
        self.visible = [native]
        with patch.object(self.surface.lineage, 'process_identity', side_effect=observe), \
                patch.object(self.surface.os, 'pidfd_open', return_value=801, create=True):
            with self.surface.FrameObserver(self.app, self.ui) as observer:
                proof = observer.observe()
                self.assertEqual(proof['main']['process'], frame)
                self.assertNotEqual(proof['main']['process'], proof['launch'])
                self.assertEqual(proof['main']['lineage'], [frame, self.identity])
                mapping[125] = {**frame, 'parent_pid': 1}
                with self.assertRaisesRegex(ValueError, 'identity changed'):
                    observer.observe()

    def dialog(self):
        dialog = self.node('/org/a11y/atspi/accessible/3', self.ui.api.ROLE_DIALOG,
                           name='Quit Painted Wolf Code?', modal=True)
        relation = Mock()
        relation.getRelationType.return_value = self.ui.api.RELATION_SUBWINDOW_OF
        relation.getNTargets.return_value = 1
        relation.getTarget.return_value = self.main
        dialog.getRelationSet.return_value = [relation]
        return dialog

    def transient(self):
        error = Exception('removed during an AT-SPI read')
        error.domain, error.code = 'atspi_error', 1
        return error

    def test_partial_relation_state_and_control_reads_roll_back_before_retry(self):
        from unittest.mock import patch
        for phase in ('relation', 'state', 'control'):
            with self.subTest(phase=phase), ExitStack() as stack:
                dialog = self.dialog()
                peer = self.node('/org/a11y/atspi/accessible/2', self.ui.api.ROLE_FRAME)
                self.visible = [self.main, peer, dialog]
                previous = self.observer.nodes.copy()
                original_control = self.observer._quit_control
                def disappear(*_):
                    self.visible = [self.main]
                    raise self.transient()
                if phase == 'relation':
                    dialog.getRelationSet.side_effect = disappear
                elif phase == 'state':
                    peer.getState.side_effect = disappear
                else:
                    control_calls = []
                    def control(*args):
                        control_calls.append(args)
                        if len(control_calls) == 1:
                            disappear()
                        return original_control(*args)
                    stack.enter_context(patch.object(self.observer, '_quit_control', side_effect=control))
                stack.enter_context(patch.object(self.surface.time, 'sleep'))
                proof = self.observer.observe()
                self.assertEqual(self.observer.nodes, previous)
                self.assertEqual([row['identity'] for row in proof['surfaces']], [proof['main']])

    def test_disappearing_dialog_keeps_only_its_previous_verified_defunct_metadata(self):
        from unittest.mock import patch
        dialog = self.dialog()
        self.visible = [self.main, dialog]
        original = self.observer.observe()['surfaces'][1]
        def disappear():
            self.visible = [self.main]
            dialog.getState().contains.side_effect = lambda state: state == self.ui.api.STATE_DEFUNCT
            raise self.transient()
        dialog.getRelationSet.side_effect = disappear
        with patch.object(self.surface.time, 'sleep'):
            closed = self.observer.observe()['surfaces'][1]
        self.assertEqual(closed['identity'], original['identity'])
        self.assertEqual(closed['parent'], original['parent'])
        self.assertTrue(closed['defunct'])
        self.assertFalse(closed['visible'])

    def test_snapshot_retries_share_one_deadline_and_programming_errors_are_terminal(self):
        from unittest.mock import patch
        for error, transient in ((self.transient(), True), (AttributeError('bug'), False)):
            with self.subTest(error=type(error).__name__), ExitStack() as stack:
                read = stack.enter_context(patch.object(self.observer, '_observe_snapshot', side_effect=error))
                stack.enter_context(patch.object(self.surface.time, 'monotonic', side_effect=[1, 3]))
                sleep = stack.enter_context(patch.object(self.surface.time, 'sleep'))
                with self.assertRaises(CaseUnavailable if transient else AttributeError):
                    self.observer.observe(deadline=2)
                read.assert_called_once_with(2)
                self.assertEqual(sleep.call_count, int(transient))

    def test_settle_passes_its_deadline_to_window_and_quit_control_reads(self):
        from unittest.mock import patch
        before = self.observer.observe()
        dialog = self.dialog()
        dialog.getState().contains.side_effect = lambda state: state in {
            self.ui.api.STATE_MODAL, self.ui.api.STATE_ACTIVE}
        self.visible, self.active = [self.main, dialog], False
        self.ui.descendants.reset_mock()
        with patch.object(self.surface.time, 'monotonic', return_value=10):
            after = self.observer.settle('workspace-request-quit-v1', before)
        self.assertTrue(surface_wire.finished('workspace-request-quit-v1', before, after))
        self.assertEqual(self.ui.windows.call_args.args, (25,))
        self.ui.descendants.assert_called_once_with(dialog, 25)

    def test_late_complete_snapshot_is_refused_without_publishing_partial_nodes(self):
        from unittest.mock import patch
        previous = self.observer.nodes.copy()
        def read(deadline):
            self.observer.nodes[('unexpected', '/partial')] = ('partial', {})
            return {'late': True}
        with patch.object(self.observer, '_observe_snapshot', side_effect=read), \
                patch.object(self.surface.time, 'monotonic', side_effect=[1, 3]):
            with self.assertRaises(CaseUnavailable):
                self.observer.observe(deadline=2)
        self.assertEqual(self.observer.nodes, previous)

    def test_transport_failure_cannot_retry_after_a_pinned_runtime_changes(self):
        from unittest.mock import patch
        def changed(deadline):
            self.identities.side_effect = lambda pid: {**self.identity, 'start_time_ticks': 99}
            raise self.transient()
        with patch.object(self.observer, '_observe_snapshot', side_effect=changed) as read, \
                patch.object(self.surface.time, 'sleep') as sleep:
            with self.assertRaisesRegex(ValueError, 'identity changed'):
                self.observer.observe()
        self.assertEqual(read.call_count, 1)
        sleep.assert_not_called()

    def test_defunct_dialog_with_no_live_application_keeps_its_verified_metadata(self):
        dialog = self.dialog()
        self.visible = [self.main, dialog]
        original = self.observer.observe()['surfaces'][1]
        self.visible = [self.main]
        dialog.app = None
        dialog.get_process_id.return_value = 0
        dialog.getState().contains.side_effect = lambda state: state == self.ui.api.STATE_DEFUNCT
        closed = self.observer.observe()
        self.assertEqual(closed['surfaces'][1]['identity'], original['identity'])
        self.assertEqual(closed['surfaces'][1]['parent'], original['parent'])
        self.assertTrue(closed['surfaces'][1]['defunct'])
        self.assertFalse(closed['surfaces'][1]['visible'])
        self.assertIsNone(closed['control'])
        self.identities.side_effect = lambda pid: {**self.identity, 'start_time_ticks': 99}
        with self.assertRaisesRegex(ValueError, 'identity changed'):
            self.observer.observe()

    def test_missing_application_without_observed_defunct_state_never_means_closed(self):
        from unittest.mock import patch
        for state_unreadable in (False, True):
            with self.subTest(state_unreadable=state_unreadable):
                dialog = self.dialog()
                self.visible = [self.main, dialog]
                self.observer.observe()
                previous = self.observer.nodes.copy()
                self.visible = [self.main]
                dialog.app = None
                if state_unreadable:
                    dialog.getState.side_effect = self.transient()
                with patch.object(self.surface.time, 'monotonic', side_effect=[1, 3]), \
                        patch.object(self.surface.time, 'sleep'):
                    with self.assertRaises(CaseUnavailable):
                        self.observer.observe(deadline=2)
                self.assertEqual(self.observer.nodes, previous)
                self.observer.nodes = {key: value for key, value in previous.items()
                                       if key == self.observer._key(self.observer.main)}

    def test_live_retained_dialog_address_replacement_is_terminal(self):
        from types import SimpleNamespace
        from unittest.mock import patch
        dialog = self.dialog()
        self.visible = [self.main, dialog]
        self.observer.observe()
        self.visible = [self.main]
        dialog.app = SimpleNamespace(bus_name=':1.99')
        with patch.object(self.surface.time, 'sleep') as sleep:
            with self.assertRaisesRegex(ValueError, 'address changed'):
                self.observer.observe()
        sleep.assert_not_called()

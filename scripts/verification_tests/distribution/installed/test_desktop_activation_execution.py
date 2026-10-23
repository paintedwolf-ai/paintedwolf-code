"""Normal public activation preserves one exact visible native frame and selected runtime."""
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases import desktop_launch as owner
from linux_release.installed_cases.records import CaseUnavailable

ROOT = {'pid': 11, 'parent_pid': 1, 'start_time_ticks': 31}
FRAME = {'pid': 12, 'parent_pid': 11, 'start_time_ticks': 32}
ENGINE = {'pid': 13, 'parent_pid': 12, 'start_time_ticks': 33}


class VisibleActivationTests(unittest.TestCase):
    def setUp(self):
        self.app = SimpleNamespace(require_running=Mock(), require_engine=Mock(),
            observations={'launch_identity': ROOT, 'engine_identity': ENGINE})
        self.active = False
        self.frame = SimpleNamespace(app=SimpleNamespace(bus_name=':1.23'), path='/org/a11y/frame',
            get_process_id=lambda: 12, getRole=lambda: 1,
            getState=lambda: SimpleNamespace(contains=lambda state: self.active))
        self.ui = SimpleNamespace(api=SimpleNamespace(ROLE_FRAME=1, STATE_ACTIVE=2),
            windows=Mock(return_value=[(FRAME, self.frame)]), visible=Mock(return_value=True))
        identities = {row['pid']: row for row in (ROOT, FRAME, ENGINE)}
        self.process = patch.object(owner, 'process_identity', side_effect=lambda pid: identities[pid])
        self.process.start()
        self.addCleanup(self.process.stop)
        self.lineage = patch('linux_release.installed_input.lineage.capture', return_value=[ROOT, FRAME])
        self.lineage.start()
        self.addCleanup(self.lineage.stop)

    def test_inactive_frame_becomes_active_only_after_one_normal_public_launch(self):
        def activate(app):
            self.assertIs(app, self.app)
            self.active = True
            return {'exit_code': 0}
        with patch.object(owner, 'activate', side_effect=activate) as launch:
            observed = owner.activate_visible_frame(self.app, self.ui)
        launch.assert_called_once_with(self.app)
        self.assertFalse(observed['before']['active'])
        self.assertTrue(observed['after']['active'])
        self.assertEqual(observed['before']['frame'], observed['after']['frame'])
        self.assertEqual(observed['before']['engine'], observed['after']['engine'])
        self.assertEqual(observed['after']['frame']['bus_name'], ':1.23')

    def test_ambiguous_or_missing_native_frame_prevents_public_launch(self):
        for frames in ([], [(FRAME, self.frame), (FRAME, self.frame)]):
            self.ui.windows.return_value = frames
            with patch.object(owner, 'activate') as launch, self.assertRaises(CaseUnavailable):
                owner.activate_visible_frame(self.app, self.ui)
            launch.assert_not_called()

    def test_same_process_with_changed_accessible_object_or_engine_is_refused(self):
        def replace_frame(app):
            self.frame.path = '/org/a11y/replacement'
            self.active = True
        with patch.object(owner, 'activate', side_effect=replace_frame), self.assertRaises(ValueError):
            owner.activate_visible_frame(self.app, self.ui)
        self.frame.path = '/org/a11y/frame'
        def replace_engine(app):
            app.observations['engine_identity'] = {**ENGINE, 'start_time_ticks': 34}
        with patch.object(owner, 'activate', side_effect=replace_engine), self.assertRaises(ValueError):
            owner.activate_visible_frame(self.app, self.ui)

    def test_inactive_or_hidden_after_launch_never_authorizes_native_input(self):
        with patch.object(owner, 'activate', return_value={'exit_code': 0}), \
                patch.object(owner.time, 'monotonic', side_effect=[0, 1, 16]), patch.object(owner.time, 'sleep'):
            with self.assertRaises(CaseUnavailable) as refused:
                owner.activate_visible_frame(self.app, self.ui)
        self.assertFalse(refused.exception.observations['public_activation']['same_frame_active'])
        self.ui.visible.return_value = False
        with patch.object(owner, 'activate') as launch, self.assertRaises(ValueError):
            owner.activate_visible_frame(self.app, self.ui)
        launch.assert_not_called()

"""Installed startup waits for native custody without entering an app password."""
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release.installed_cases import desktop_ui
from scripts.linux_release.installed_cases.app import InstalledApp
from scripts.linux_release.installed_cases.records import CaseFailed, CaseUnavailable

IDENTITY = {'pid': 123, 'parent_pid': 100, 'start_time_ticks': 42}


class InstalledStartupTests(unittest.TestCase):
    def app(self):
        app = InstalledApp({'artifact': {}, 'source_commit': 'a' * 40}, timeout=1)
        app.process = SimpleNamespace(poll=lambda: None)
        app.launch = Mock()
        app.stop = Mock()
        return app

    def test_normal_startup_reaches_owned_readiness_without_accessible_input(self):
        app = self.app()
        app.try_ready = Mock(side_effect=[False, True])
        with patch.object(desktop_ui, 'accessibility_bus') as accessibility, patch(
                'scripts.linux_release.installed_cases.app.time.monotonic',
                side_effect=[0, 0, 0.1]), patch(
                'scripts.linux_release.installed_cases.app.time.sleep'):
            self.assertIs(app.start(), app)
        app.launch.assert_called_once_with()
        self.assertEqual(app.try_ready.call_count, 2)
        app.stop.assert_not_called()
        accessibility.assert_not_called()

    def test_unready_custody_keeps_its_deadline_and_stops_without_input(self):
        app = self.app()
        app.process = SimpleNamespace(poll=lambda: None)
        app.try_ready = Mock(return_value=False)
        with patch.object(desktop_ui, 'accessibility_bus') as accessibility, patch(
                'scripts.linux_release.installed_cases.app.time.monotonic',
                side_effect=[0, 0, 2]), patch(
                'scripts.linux_release.installed_cases.app.time.sleep'):
            with self.assertRaises(CaseUnavailable) as failure:
                app.start()
        self.assertIs(failure.exception.observations, app.observations)
        self.assertEqual(failure.exception.streams, {'app-stdout': b'', 'app-stderr': b''})
        self.assertGreaterEqual(app.stop.call_count, 1)
        accessibility.assert_not_called()

    def test_product_failure_and_unexpected_error_stop_the_owned_launch(self):
        for error in (CaseFailed('product exited'), RuntimeError('fixture transport')):
            app = self.app()
            app.wait_ready = Mock(side_effect=error)
            with self.subTest(kind=type(error).__name__), self.assertRaises(type(error)) as failure:
                app.start()
            self.assertIs(failure.exception, error)
            app.stop.assert_called_once_with()
            if isinstance(error, CaseFailed):
                self.assertIs(error.observations, app.observations)
                self.assertEqual(error.streams, {'app-stdout': b'', 'app-stderr': b''})

    def test_foreign_or_reused_application_pid_is_never_an_owned_window(self):
        app = SimpleNamespace(observations={'launch_identity': IDENTITY})
        with patch.object(desktop_ui, 'process_identity', return_value=dict(IDENTITY, start_time_ticks=43)):
            self.assertFalse(desktop_ui.owned_process(123, app))
        with patch.object(desktop_ui, 'process_identity', side_effect=[
                {'pid': 200, 'parent_pid': 123, 'start_time_ticks': 7}, IDENTITY]):
            self.assertTrue(desktop_ui.owned_process(200, app))

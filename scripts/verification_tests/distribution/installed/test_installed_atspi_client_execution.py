"""The installed desktop cases use the harness AT-SPI client setup."""
import os
import sys
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import linux_release
from linux_release.installed_cases import desktop_ui as ui


class AtspiClientTests(unittest.TestCase):
    def test_sibling_script_packages_resolve_from_this_runner_root(self):
        self.assertEqual(linux_release.SCRIPTS_PACKAGE, '')
        client = linux_release.scripts_module('harness.atspi_client')
        self.assertEqual(client.__name__, 'harness.atspi_client')

    def test_session_bus_points_at_the_graphical_session_and_restores(self):
        session = {'DBUS_SESSION_BUS_ADDRESS': 'unix:path=/run/user/1000/bus', 'XDG_RUNTIME_DIR': '/run/user/1000'}
        with patch.dict(os.environ, {'XDG_RUNTIME_DIR': '/run/user/0'}, clear=False), \
                patch.object(ui, 'session_environment', return_value=session):
            os.environ.pop('DBUS_SESSION_BUS_ADDRESS', None)
            with ui.accessibility_bus({'leader': 1}):
                self.assertEqual(os.environ['DBUS_SESSION_BUS_ADDRESS'], session['DBUS_SESSION_BUS_ADDRESS'])
                self.assertEqual(os.environ['XDG_RUNTIME_DIR'], '/run/user/1000')
            self.assertNotIn('DBUS_SESSION_BUS_ADDRESS', os.environ)
            self.assertEqual(os.environ['XDG_RUNTIME_DIR'], '/run/user/0')

    def test_client_library_loads_with_bounded_timeouts_or_is_unavailable(self):
        atspi = SimpleNamespace(set_timeout=Mock())
        gi = ModuleType('gi')
        gi.require_version = Mock()
        repository = ModuleType('gi.repository')
        repository.Atspi = atspi
        pyatspi = ModuleType('pyatspi')
        with patch.dict(sys.modules, {'gi': gi, 'gi.repository': repository, 'pyatspi': pyatspi}):
            self.assertIs(ui.load_api(), pyatspi)
        gi.require_version.assert_called_once_with('Atspi', '2.0')
        atspi.set_timeout.assert_called_once_with(1000, 5000)
        gi.require_version = Mock(side_effect=ValueError('Namespace Atspi not available'))
        with patch.dict(sys.modules, {'gi': gi, 'gi.repository': repository}), self.assertRaises(ui.CaseUnavailable):
            ui.load_api()

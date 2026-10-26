"""Passive notification monitoring pins both bus authority and native process identity."""
from types import SimpleNamespace
from contextlib import ExitStack
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases.notification_bus import NotificationBus
from linux_release.installed_cases.notification_delivery import PATH, SERVICE
from linux_release.installed_cases.records import CaseFailed, CaseUnavailable
from verification_tests.distribution.installed.test_notification_delivery_execution import message, capability_messages


class NotificationBusTests(unittest.TestCase):
    def setUp(self):
        self.ui = {'pid': 100, 'parent_pid': 99, 'start_time_ticks': 200}
        self.service = {'pid': 200, 'parent_pid': 1, 'start_time_ticks': 300}
        self.monitor = NotificationBus(Mock(), self.ui)
        self.monitor.owner, self.monitor.service_process = ':1.7', self.service

    def test_replaced_owner_and_reused_ui_pid_are_terminal(self):
        self.monitor._call = Mock(return_value=':1.8')
        with patch('linux_release.installed_cases.notification_bus.process_identity', return_value=self.ui):
            with self.assertRaises(CaseFailed):
                self.monitor.require_owners()
        with patch('linux_release.installed_cases.notification_bus.process_identity',
                return_value={**self.ui, 'start_time_ticks': 201}):
            with self.assertRaises(CaseFailed):
                self.monitor.require_owners()

    def test_refuses_service_process_replacement_even_with_same_unique_bus_name(self):
        self.monitor._call = Mock(return_value=':1.7')
        self.monitor._pid = Mock(return_value={**self.service, 'start_time_ticks': 301})
        with patch('linux_release.installed_cases.notification_bus.process_identity', return_value=self.ui):
            with self.assertRaises(CaseFailed):
                self.monitor.require_owners()

    def test_foreign_notification_is_not_a_product_delivery(self):
        self.monitor.require_owners = Mock()
        self.monitor._call = Mock(return_value=':1.7')
        self.monitor._pid = Mock(side_effect=lambda sender, deadline=None: self.ui if sender == ':1.12' else self.service)
        self.monitor.rows.extend([message('call', sender=':1.13'), *capability_messages(), message('call'),
            message('return', reply_serial=10, signature='(u)', body=(41,))])
        self.monitor.drain()
        self.assertEqual(self.monitor.trace.sender, ':1.12')
        self.assertEqual(self.monitor.trace.delivery['notification_id'], 41)
        self.assertEqual(self.monitor.require_owners.call_count, 2)

    def test_second_native_ui_connection_cannot_be_ignored_as_foreign(self):
        self.monitor.require_owners = Mock()
        self.monitor._call = Mock(return_value=':1.7')
        self.monitor._pid = Mock(return_value=self.ui)
        self.monitor.rows.extend([*capability_messages(), message('call'), message('call', sender=':1.13')])
        with self.assertRaises(CaseFailed):
            self.monitor.drain()
        self.assertIsNotNone(self.monitor.trace)

    def test_capture_retains_bounded_callback_failure_for_main_observer(self):
        self.monitor.Gio = SimpleNamespace(DBusMessageType=SimpleNamespace(METHOD_CALL=1,METHOD_RETURN=2,SIGNAL=3))
        self.monitor.monitoring = True
        raw = Mock()
        raw.get_message_type.return_value = 1
        raw.get_interface.return_value = SERVICE
        raw.get_member.return_value = 'Notify'
        raw.get_path.return_value = PATH
        raw.get_sender.return_value = ':1.12'
        raw.get_destination.return_value = ':1.7'
        raw.get_body.return_value.get_size.return_value = (64 << 10) + 1
        self.assertIsNone(self.monitor._filter(None, raw, True, None))
        self.monitor.require_owners = Mock()
        with self.assertRaises(ValueError):
            self.monitor.drain()
        self.assertEqual(len(self.monitor.rows), 0)

    def test_monitor_never_answers_an_observed_call(self):
        self.monitor.Gio = SimpleNamespace(DBusMessageType=SimpleNamespace(METHOD_CALL=1,METHOD_RETURN=2,SIGNAL=3))
        self.monitor.monitoring = True
        raw = Mock()
        raw.get_message_type.return_value = 1
        raw.get_interface.return_value = 'other.Interface'
        self.assertIsNone(self.monitor._filter(None, raw, True, None))
        self.assertIs(self.monitor._filter(None, raw, False, None), raw)

    def test_connection_cleanup_closes_only_the_monitor_and_control_connections(self):
        control, monitor = Mock(), Mock()
        self.monitor.control, self.monitor.monitor, self.monitor.filter_id = control, monitor, 12
        self.monitor.close()
        monitor.remove_filter.assert_called_once_with(12)
        monitor.close_sync.assert_called_once_with(None)
        control.close_sync.assert_called_once_with(None)
        self.assertIsNone(self.monitor.monitor)
        self.assertIsNone(self.monitor.control)

    def test_monitor_cleanup_failure_still_closes_control_and_retains_failure(self):
        control, monitor = Mock(), Mock()
        monitor.close_sync.side_effect = OSError('connection unavailable')
        self.monitor.control, self.monitor.monitor = control, monitor
        with self.assertRaises(CaseUnavailable) as raised:
            self.monitor.close()
        control.close_sync.assert_called_once_with(None)
        self.assertEqual(raised.exception.observations['notification_monitor_cleanup'][0]['type'], 'OSError')

    def test_cold_owner_is_pinned_only_from_the_native_ui_real_notify(self):
        self.monitor.owner,self.monitor.service_process=None,None
        self.monitor.require_owners=Mock()
        self.monitor._call=Mock(return_value=':1.7')
        self.monitor._pid=Mock(side_effect=lambda sender, deadline=None:self.ui if sender==':1.12' else self.service)
        self.monitor.drain()
        self.monitor._call.assert_not_called()
        self.monitor.rows.extend([*capability_messages(),message('call'),message('return',reply_serial=10,signature='(u)',body=(41,))])
        self.monitor.drain()
        self.assertEqual(self.monitor.owner,':1.7')
        self.assertEqual(self.monitor.service_process,self.service)
        self.assertEqual(self.monitor.trace.delivery['notification_id'],41)

    def test_foreign_return_headers_are_dropped_without_body_inspection(self):
        self.monitor.Gio=SimpleNamespace(DBusMessageType=SimpleNamespace(METHOD_CALL=1,METHOD_RETURN=2,SIGNAL=3))
        self.monitor.monitoring=True
        self.monitor.pending_calls[(':1.12',10)] = ':1.7'
        raw=Mock()
        raw.get_message_type.return_value=2
        raw.get_destination.return_value=':1.13'
        raw.get_reply_serial.return_value=10
        self.assertIsNone(self.monitor._filter(None,raw,True,None))
        raw.get_body.assert_not_called()
        self.assertEqual(len(self.monitor.rows),0)

    def test_foreign_service_reply_body_is_not_read_even_with_matching_serial(self):
        self.monitor.Gio = SimpleNamespace(DBusMessageType=SimpleNamespace(METHOD_CALL=1,METHOD_RETURN=2,SIGNAL=3))
        self.monitor.monitoring = True
        self.monitor.pending_calls[(':1.12', 8)] = ':1.7'
        raw = Mock()
        raw.get_message_type.return_value = 2
        raw.get_sender.return_value = ':1.99'
        raw.get_destination.return_value = ':1.12'
        raw.get_reply_serial.return_value = 8
        self.monitor._filter(None, raw, True, None)
        raw.get_body.assert_not_called()
        self.assertEqual(len(self.monitor.rows), 0)

    def test_actual_empty_capability_request_is_captured_without_emitting_any_call(self):
        self.monitor.Gio = SimpleNamespace(DBusMessageType=SimpleNamespace(METHOD_CALL=1,METHOD_RETURN=2,SIGNAL=3))
        self.monitor.monitoring = True
        raw = Mock()
        for name, value in {'message_type':1, 'sender':':1.12', 'destination':':1.7', 'serial':8,
                'reply_serial':0, 'path':PATH, 'interface':SERVICE, 'member':'GetCapabilities', 'body':None}.items():
            getattr(raw, 'get_' + name).return_value = value
        self.monitor._filter(None, raw, True, None)
        self.assertEqual(self.monitor.rows[0]['signature'], '()')
        self.assertEqual(self.monitor.rows[0]['body'], ())
        self.assertIsNone(self.monitor.trace)

    def test_passive_capture_starts_before_ordinary_provider_setup_and_closes_on_failure(self):
        from linux_release.installed_cases import notification_activation as case
        app, frames, bus, fixture = Mock(), Mock(), Mock(), Mock()
        app.__enter__, app.__exit__ = Mock(return_value=app), Mock(return_value=False)
        app.observations, app.streams.return_value = {}, {}
        frames.main = {'process':self.ui}
        frames.__enter__, frames.__exit__ = Mock(return_value=frames), Mock(return_value=False)
        bus.__enter__, bus.__exit__ = Mock(return_value=bus), Mock(return_value=False)
        bus.snapshot.return_value = {}
        fixture.__enter__, fixture.__exit__ = Mock(return_value=fixture), Mock(return_value=False)
        fixture.observations = []
        ui = Mock()
        ui.__enter__, ui.__exit__ = Mock(return_value=ui), Mock(return_value=False)
        def setup(*args):
            bus.__enter__.assert_called_once()
            # A real native exchange during ordinary setup reaches the existing passive trace.
            for row in capability_messages():self.monitor.rows.append(row)
            self.monitor.drain()
            self.assertTrue(self.monitor.trace.capabilities['body-markup'])
            raise CaseUnavailable('bounded fixture stop before any turn or notification')
        self.monitor.require_owners = Mock()
        self.monitor._call = Mock(return_value=':1.7')
        self.monitor._pid = Mock(side_effect=lambda sender, deadline=None:self.ui if sender==':1.12' else self.service)
        with tempfile.TemporaryDirectory() as directory, ExitStack() as stack:
            for name, value in {'load_provider_recovery':None, 'ResponseBarrier':Mock(), 'InstalledApp':app,
                    'protected_launcher':{}, 'interactive_window':{}, 'DesktopUI':ui, 'FrameObserver':frames,
                    'NotificationBus':bus}.items():
                stack.enter_context(patch.object(case, name, return_value=value))
            stack.enter_context(patch.object(case.ProviderFixture, 'gui', return_value=fixture))
            stack.enter_context(patch.object(case, 'configure_provider', side_effect=setup))
            turn = stack.enter_context(patch.object(case, 'submit_project_turn'))
            with self.assertRaises(CaseUnavailable):
                case.execute({'native_input':{}}, 'notifications', 'activation-token-and-chat', Path(directory))
            turn.assert_not_called()
        bus.__exit__.assert_called_once()


if __name__ == '__main__':
    unittest.main()

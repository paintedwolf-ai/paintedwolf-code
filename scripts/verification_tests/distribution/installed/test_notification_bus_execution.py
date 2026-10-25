"""Passive notification monitoring pins both bus authority and native process identity."""
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases.notification_bus import NotificationBus
from linux_release.installed_cases.notification_delivery import PATH, SERVICE
from linux_release.installed_cases.records import CaseFailed, CaseUnavailable
from verification_tests.distribution.installed.test_notification_delivery_execution import message


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
        self.monitor.rows.extend([message('call', sender=':1.13'), message('call'),
            message('return', reply_serial=10, signature='(u)', body=(41,))])
        self.monitor.drain()
        self.assertEqual(self.monitor.trace.sender, ':1.12')
        self.assertEqual(self.monitor.trace.delivery['notification_id'], 41)
        self.assertEqual(self.monitor.require_owners.call_count, 2)

    def test_second_native_ui_connection_cannot_be_ignored_as_foreign(self):
        self.monitor.require_owners = Mock()
        self.monitor._call = Mock(return_value=':1.7')
        self.monitor._pid = Mock(return_value=self.ui)
        self.monitor.rows.extend([message('call'), message('call', sender=':1.13')])
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
        self.monitor.rows.extend([message('call'),message('return',reply_serial=10,signature='(u)',body=(41,))])
        self.monitor.drain()
        self.assertEqual(self.monitor.owner,':1.7')
        self.assertEqual(self.monitor.service_process,self.service)
        self.assertEqual(self.monitor.trace.delivery['notification_id'],41)

    def test_foreign_return_headers_are_dropped_without_body_inspection(self):
        self.monitor.Gio=SimpleNamespace(DBusMessageType=SimpleNamespace(METHOD_CALL=1,METHOD_RETURN=2,SIGNAL=3))
        self.monitor.monitoring=True
        self.monitor.pending_calls.add((':1.12',10))
        raw=Mock()
        raw.get_message_type.return_value=2
        raw.get_destination.return_value=':1.13'
        raw.get_reply_serial.return_value=10
        self.assertIsNone(self.monitor._filter(None,raw,True,None))
        raw.get_body.assert_not_called()
        self.assertEqual(len(self.monitor.rows),0)


if __name__ == '__main__':
    unittest.main()

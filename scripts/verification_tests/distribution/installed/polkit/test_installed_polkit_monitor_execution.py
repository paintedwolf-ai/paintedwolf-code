"""The private monitor retains only bounded typed causal messages."""
import threading
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from scripts.linux_release.installed_input import polkit_monitor as monitor
from scripts.linux_release.installed_input.polkit_transaction import AUTHORITY, AGENT


class Body:
    def __init__(self, signature, value, size=100):
        self.signature, self.value, self.size = signature, value, size

    def get_type_string(self):
        return self.signature

    def unpack(self):
        return self.value

    def get_size(self):
        return self.size


class Message:
    def __init__(self, kind=1, *, interface=AUTHORITY, member='CheckAuthorization',
                 body=None, sender=':1.3', destination=':1.2', serial=11, reply_serial=0):
        self.values = {'message_type': kind, 'interface': interface, 'member': member,
            'body': body, 'sender': sender, 'destination': destination, 'serial': serial,
            'reply_serial': reply_serial, 'path': '/agent', 'error_name': None}

    def __getattr__(self, key):
        return lambda: self.values[key.removeprefix('get_')]


def observer():
    value = monitor.Monitor.__new__(monitor.Monitor)
    value.reader = SimpleNamespace(gio=SimpleNamespace(DBusMessageType=SimpleNamespace(
        METHOD_CALL=1, METHOD_RETURN=2, ERROR=3, SIGNAL=4)))
    value.rows, value.calls = [], set()
    value.closed, value.failure = False, None
    value.lock = threading.RLock()
    value.deadline = time.monotonic() + 10
    return value


def check(action=monitor.ACTION):
    return Message(body=Body(monitor.SIGNATURES[(AUTHORITY, 'CheckAuthorization')],
        [('unix-process', {'pid': 401, 'start-time': 71, 'uid': 30059}), action, {}, 1, 'uuid']))


class PolkitMonitorTests(unittest.TestCase):
    def test_marked_account_validation_precedes_any_bus_connection(self):
        with patch.object(monitor, 'validate_account', side_effect=ValueError('fixture')), patch.object(
                monitor, 'Reader') as reader:
            with self.assertRaises(ValueError):
                monitor.Monitor({}, 'vm')
            reader.assert_not_called()

    def test_action_and_reply_inventory_filter_unrelated_messages(self):
        value = observer()
        self.assertIsNone(value._decode(check('other.action')))
        self.assertIsNone(value._decode(Message(kind=2, reply_serial=11)))
        self.assertEqual(value._decode(check())['member'], 'CheckAuthorization')
        returned = Message(kind=2, sender=':1.2', destination=':1.3', reply_serial=11,
                           body=Body('((bba{ss}))', [(False, True, {})]))
        self.assertEqual(value._decode(returned)['reply_serial'], 11)
        self.assertIsNone(value._decode(returned))

    def test_typed_shape_and_memory_bounds_fail_closed_without_error_prose(self):
        for invalid in (Message(body=Body('(s)', ['private-cookie'])),
                        Message(body=Body('(s)', ['private-cookie'], size=16385))):
            value = observer()
            self.assertIs(value._receive(None, invalid, True), invalid)
            self.assertEqual(value.rows, [])
            self.assertEqual(value.failure, 'Polkit observation lost its typed causal transport')
            self.assertNotIn('private-cookie', value.failure)
        value = observer()
        value.rows = [None] * 128
        value._receive(None, check(), True)
        self.assertIsNotNone(value.failure)
        self.assertEqual(value.rows, [])

    def test_agent_cookie_remains_private_and_only_authoritative_owner_signal_is_retained(self):
        value = observer()
        begin = Message(interface=AGENT, member='BeginAuthentication', body=Body(
            monitor.SIGNATURES[(AGENT, 'BeginAuthentication')], [monitor.ACTION,
            'localized message', '', {}, 'private-cookie', []]))
        self.assertEqual(value._decode(begin)['body'][4], 'private-cookie')
        signal = Message(kind=4, interface=monitor.DBUS, member='NameOwnerChanged',
                         sender=':1.foreign', body=Body('(sss)', [':1.3', ':1.3', '']))
        self.assertIsNone(value._decode(signal))
        signal.values['sender'] = monitor.DBUS
        self.assertEqual(value._decode(signal)['kind'], 'owner-changed')

    def test_inflight_authentication_exposes_no_second_input_target_or_early_success(self):
        from unittest.mock import Mock
        for field in ('response', 'agent_returned', 'cancel_requested'):
            value = observer()
            value.reader.timeout_ms = Mock(return_value=100)
            value._authority = Mock()
            transaction = Mock(finished=False, begin={'serial': 1}, response=None,
                               agent_returned=False, cancel_requested=False)
            setattr(transaction, field, {'serial': 2} if field == 'response' else True)
            value.transaction = transaction
            self.assertIsNone(value.observe())
            transaction.pending.assert_not_called()
            transaction.witness.assert_not_called()

    def test_late_or_outgoing_messages_cannot_extend_the_monitor_lifetime(self):
        value = observer()
        value.deadline = time.monotonic() - 1
        self.assertIs(value._receive(None, check(), False).__class__, Message)
        self.assertIsNone(value.failure)
        value._receive(None, check(), True)
        self.assertIsNotNone(value.failure)


if __name__ == '__main__':
    unittest.main()

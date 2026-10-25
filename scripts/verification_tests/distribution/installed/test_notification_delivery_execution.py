"""A notification action is proof only after a real owner-bound delivery and physical arm."""
import copy
import unittest

from scripts.linux_release.installed_cases.notification_delivery import DeliveryTrace, PATH, SERVICE
from scripts.linux_release.installed_cases.records import CaseFailed, CaseUnavailable


def message(kind, **values):
    row = {'type': kind, 'sender': ':1.12' if kind == 'call' else ':1.7',
        'destination': ':1.7' if kind == 'call' else ':1.12', 'serial': 10,
        'reply_serial': 0, 'path': PATH, 'interface': SERVICE, 'member': 'Notify',
        'signature': '(susssasa{sv}i)', 'body': ('Painted Wolf Code', 0, '', 'Fixture',
            'Finished', ['default', 'Open chat'], {'desktop-entry': 'dev.paintedwolf.code'}, -1)}
    row.update(values)
    return row


def delivered(trace):
    trace.observe(message('call'))
    trace.observe(message('return', reply_serial=10, signature='(u)', body=(41,)))


def signal(name, value, identity=41):
    return message('signal', member=name, signature='(us)', body=(identity, value))


class NotificationDeliveryTests(unittest.TestCase):
    def setUp(self):
        self.trace = DeliveryTrace(':1.12', ':1.7', 'dev.paintedwolf.code')

    def test_real_delivery_token_then_action_after_physical_arm(self):
        delivered(self.trace)
        self.trace.arm()
        self.trace.observe(signal('ActivationToken', 'wayland-token'))
        self.trace.observe(signal('ActionInvoked', 'default'))
        proof = self.trace.snapshot()
        self.assertEqual(proof['delivery']['notification_id'], 41)
        self.assertEqual(proof['activation']['notification_id'], 41)
        self.assertLess(proof['activation']['token']['sequence'], proof['activation']['sequence'])
        self.assertNotIn('wayland-token', str(proof))
        self.assertNotIn('Finished', str(proof))

    def test_reply_requires_exact_serial_and_unique_service_owner(self):
        self.trace.observe(message('call'))
        self.trace.observe(message('return', reply_serial=9, signature='(u)', body=(72,)))
        self.assertIsNone(self.trace.snapshot()['delivery'])
        with self.assertRaises(CaseUnavailable):
            self.trace.arm()
        with self.assertRaises(CaseFailed):
            self.trace.observe(message('return', sender=':1.8', reply_serial=10, signature='(u)', body=(41,)))

    def test_refuses_foreign_sender_replacement_and_second_notification(self):
        for change in ({'sender': ':1.13'}, {'destination': ':1.8'}):
            with self.subTest(change=change), self.assertRaises(CaseFailed):
                self.trace.observe(message('call', **change))
        self.trace.observe(message('call'))
        with self.assertRaises(CaseUnavailable):
            self.trace.observe(message('call', serial=11))

    def test_requires_declared_desktop_entry_actions_and_fresh_identity(self):
        original = message('call')
        for index, replacement in ((1, 19), (5, []), (6, {'desktop-entry': 'foreign.app'})):
            changed = copy.deepcopy(original)
            changed['body'] = list(changed['body'])
            changed['body'][index] = replacement
            with self.subTest(index=index), self.assertRaises(CaseUnavailable):
                self.trace.observe(changed)
        self.assertIsNone(self.trace.call)

    def test_unarmed_action_is_not_proof(self):
        delivered(self.trace)
        with self.assertRaises(CaseFailed):
            self.trace.observe(signal('ActionInvoked', 'default'))

    def test_unknown_id_and_foreign_owner_cannot_activate(self):
        delivered(self.trace)
        self.trace.arm()
        self.trace.observe(signal('ActivationToken', 'foreign', 42))
        self.trace.observe(signal('ActionInvoked', 'default', 42))
        self.assertIsNone(self.trace.activation)
        changed = signal('ActivationToken', 'token')
        changed['sender'] = ':1.8'
        with self.assertRaises(CaseFailed):
            self.trace.observe(changed)

    def test_requires_token_before_default_action_and_refuses_replays(self):
        delivered(self.trace)
        self.trace.arm()
        with self.assertRaises(CaseFailed):
            self.trace.observe(signal('ActionInvoked', 'default'))
        self.trace.observe(signal('ActivationToken', 'token'))
        with self.assertRaises(CaseFailed):
            self.trace.observe(signal('ActionInvoked', 'other'))
        self.trace.observe(signal('ActionInvoked', 'default'))
        with self.assertRaises(CaseFailed):
            self.trace.observe(signal('ActionInvoked', 'default'))

    def test_closed_notification_cannot_authorize_an_action(self):
        delivered(self.trace)
        self.trace.arm()
        with self.assertRaises(CaseUnavailable):
            self.trace.observe(message('signal', member='NotificationClosed', signature='(uu)', body=(41, 2)))

    def test_refuses_invalid_delivered_identifier(self):
        for identity in (0, True, -1, 1 << 32):
            trace = DeliveryTrace(':1.12', ':1.7', 'dev.paintedwolf.code')
            trace.observe(message('call'))
            with self.subTest(identity=identity), self.assertRaises(ValueError):
                trace.observe(message('return', reply_serial=10, signature='(u)', body=(identity,)))


if __name__ == '__main__':
    unittest.main()

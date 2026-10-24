import os
import unittest
from unittest.mock import patch

from scripts.linux_release.installed_cases.logind_context import CallFailure
from scripts.linux_release.installed_cases.native_session import NativeSessionObserver
from scripts.linux_release.installed_cases.records import CaseUnavailable


class Variant:
    def __init__(self, value, signature=None):
        self.value, self.signature = value, signature

    def unpack(self):
        return self.value

    def get_child_value(self, index):
        return self.value[index]

    def get_variant(self):
        return self

    def get_type_string(self):
        return self.signature


class NativeReader:
    def __init__(self):
        self.closes = 0
        self.names = {'org.freedesktop.login1': ':1.1', 'org.gnome.ScreenSaver': ':1.2'}
        self.pids = {':1.1': 100, ':1.2': 200}
        self.properties = {'Id': ('s', '7'), 'User': ('(uo)', (os.getuid(), '/org/freedesktop/login1/user/_1000')),
            'Seat': ('(so)', ('seat0', '/org/freedesktop/login1/seat/seat0')), 'Type': ('s', 'wayland'),
            'TimestampMonotonic': ('t', 123456), 'Remote': ('b', False), 'Active': ('b', True), 'LockedHint': ('b', False)}
        self.path = '/org/freedesktop/login1/session/_7'
        self.locked = False
        self.rows = [('sleep', 'private diagnostic', 'private diagnostic', 'block', os.getuid(), 300)]
        self.calls = []

    def close(self):
        self.closes += 1

    def call(self, path, interface, method, args, signature, *, user=False, destination=None):
        self.calls.append((destination, method, user))
        if method == 'GetNameOwner':
            name = args[1][0]
            if name not in self.names:
                raise CallFailure('org.freedesktop.DBus.Error.NameHasNoOwner')
            return Variant((self.names[name],))
        if method == 'GetConnectionUnixProcessID':
            return Variant((self.pids[args[1][0]],))
        if method == 'GetSession':
            return Variant((self.path,))
        if method == 'Get':
            kind, value = self.properties[args[1][1]]
            return Variant((Variant(value, kind),))
        if method == 'GetActive':
            return Variant((self.locked,))
        if method == 'ListInhibitors':
            return Variant((self.rows,))
        raise AssertionError(method)


class NativeSessionTests(unittest.TestCase):
    def setUp(self):
        self.reader = NativeReader()
        self.session = {'id': '7', 'User': str(os.getuid()), 'Type': 'wayland',
            'Active': 'yes', 'Remote': 'no', 'Seat': 'seat0', 'Desktop': 'GNOME'}
        self.births = {100: 10, 200: 20, 300: 30}

    def owner(self, pid, uid):
        return {'pid': pid, 'parent_pid': 1, 'start_time_ticks': self.births[pid], 'uid': uid}

    def observer(self):
        return NativeSessionObserver(self.session, reader=self.reader, owner=self.owner, shell=lambda: {
            'unique_owner': ':1.2', 'pid': 200, 'start_time_ticks': 20,
            'executable': '/usr/bin/gnome-shell', 'device': 1, 'inode': 2, 'mode': 0o755, 'bytes': 3})

    def test_owned_reader_closes_on_context_exit_and_failed_initial_capture(self):
        borrowed = self.observer()
        shell = borrowed.shell
        borrowed.close()
        self.assertEqual(self.reader.closes, 0)
        with patch('scripts.linux_release.installed_cases.native_session.Reader', return_value=self.reader):
            with NativeSessionObserver(self.session, owner=self.owner, shell=shell) as owned:
                self.assertFalse(owned.closed)
            owned.close()
            self.assertEqual(self.reader.closes, 1)
            with self.assertRaises(ValueError):
                owned.observe()
            self.reader.properties['TimestampMonotonic'] = ('t', 0)
            with self.assertRaises(ValueError):
                NativeSessionObserver(self.session, owner=self.owner, shell=shell)
            self.assertEqual(self.reader.closes, 2)

    def test_independent_session_locker_and_kernel_inhibitor_facts(self):
        observer = self.observer()
        self.reader.locked = True
        self.reader.properties['LockedHint'] = ('b', True)
        result = observer.inhibitors()
        self.assertTrue(result['native_session']['locker_active'])
        self.assertTrue(result['native_session']['locked_hint'])
        self.assertEqual(result['inhibitors'], [{'what': 'sleep', 'mode': 'block',
            'owner': self.owner(300, os.getuid())}])
        self.assertNotIn('private diagnostic', repr(result))
        self.assertIn((':1.1', 'ListInhibitors', False), self.reader.calls)
        self.assertIn((':1.2', 'GetActive', True), self.reader.calls)

    def test_screenshield_bus_name_binds_locker_when_screensaver_proxy_is_separate(self):
        self.reader.names['org.gnome.ScreenSaver'] = ':1.9'
        self.reader.pids[':1.9'] = 250
        self.births[250] = 25
        self.reader.names['org.gnome.Shell.ScreenShield'] = ':1.2'
        observed = self.observer().observe()
        self.assertEqual(observed['authorities']['locker']['name'], 'org.gnome.Shell.ScreenShield')
        self.assertEqual(observed['authorities']['locker']['unique'], ':1.2')
        del self.reader.names['org.gnome.ScreenSaver']
        self.assertEqual(self.observer().observe()['authorities']['locker']['name'], 'org.gnome.Shell.ScreenShield')
        self.reader.names['org.gnome.ScreenSaver'] = 'invalid'
        with self.assertRaises(ValueError):
            self.observer()

    def test_replaced_bus_owner_or_reused_pid_refuses(self):
        for change in ('bus', 'birth'):
            with self.subTest(change=change):
                self.setUp()
                observer = self.observer()
                if change == 'bus':
                    self.reader.names['org.gnome.ScreenSaver'] = ':1.3'
                    self.reader.pids[':1.3'] = 200
                else:
                    self.births[200] += 1
                with self.assertRaises(ValueError):
                    observer.observe()

    def test_session_reassignment_and_malformed_types_refuse(self):
        for name, value in [('Seat', ('(so)', ('seat1', '/seat'))),
                            ('User', ('(uo)', (os.getuid() + 1, '/user'))),
                            ('Active', ('b', False)), ('LockedHint', ('s', 'true'))]:
            with self.subTest(name=name):
                self.setUp()
                observer = self.observer()
                self.reader.properties[name] = value
                with self.assertRaises(ValueError):
                    observer.observe()

    def test_unverified_native_adapter_is_explicit(self):
        self.session['Desktop'] = 'KDE'
        with self.assertRaises(CaseUnavailable):
            self.observer()

    def test_inhibitor_shapes_and_owner_disappearance_refuse(self):
        observer = self.observer()
        for rows in [[('sleep', '', '', 'block', True, 300)], [()] , self.reader.rows * 257]:
            self.reader.rows = rows
            with self.assertRaises(ValueError):
                observer.inhibitors()
        self.reader.rows = [('sleep', '', '', 'block', os.getuid(), 999)]
        with self.assertRaises(KeyError):
            observer.inhibitors()

    def test_observation_cannot_mutate_captured_session_authority(self):
        observer = self.observer()
        result = observer.observe()
        result['session']['id'] = '8'
        result['authorities']['locker']['process']['start_time_ticks'] = 1
        self.assertEqual(observer.observe()['session']['id'], '7')

    def test_same_session_id_recreation_and_changed_object_links_refuse(self):
        for change in ('created', 'object', 'user', 'seat'):
            with self.subTest(change=change):
                self.setUp()
                observer = self.observer()
                if change == 'created':
                    self.reader.properties['TimestampMonotonic'] = ('t', 123457)
                elif change == 'object':
                    self.reader.path += '_new'
                elif change == 'user':
                    self.reader.properties['User'] = ('(uo)', (os.getuid(), '/org/freedesktop/login1/user/new'))
                else:
                    self.reader.properties['Seat'] = ('(so)', ('seat0', '/org/freedesktop/login1/seat/new'))
                with self.assertRaises(ValueError):
                    observer.observe()


if __name__ == '__main__':
    unittest.main()

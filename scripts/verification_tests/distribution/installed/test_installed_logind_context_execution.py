"""Graphical execution is an actual session or an attested user-manager unit."""
import unittest

from linux_release.installed_cases.logind_context import observed, CallFailure, LOGIN, CaseUnavailable

SESSION = '/org/freedesktop/login1/session/owned'
OWNER = '/org/freedesktop/login1/user/owned'
UNIT = '/org/freedesktop/systemd1/unit/owned'
GROUP = '/user.slice/user-1000.slice/user@1000.service/app.slice/owned.service'


class Reader:
    def __init__(self, direct=True):
        self.direct = direct
        self.values = {'Id': 'owned.service', 'ControlGroup': GROUP, 'UID': 1000, 'Display': ('1', SESSION),
                       'User': (1000, OWNER), 'Remote': False, 'Active': True, 'Type': 'wayland'}
        self.calls = []

    def object(self, method, pid, *, user=False):
        self.calls.append((method, pid, user))
        if method == 'GetSessionByPID':
            if self.direct:
                return SESSION
            raise CallFailure(LOGIN + '.NoSessionForPID')
        return {'GetUnitByPID': UNIT, 'GetUserByPID': OWNER}[method]

    def property(self, path, interface, name, signature, *, user=False):
        if name == 'ControlGroup':
            expected = 'org.freedesktop.systemd1.' + ('Scope' if self.values['Id'].endswith('.scope') else 'Service')
            if interface != expected:
                raise AssertionError('ControlGroup belongs to the concrete unit interface')
        return self.values[name]


class ExecutionContextTests(unittest.TestCase):
    def test_direct_remote_session_cannot_be_replaced_with_local_user_display(self):
        reader = Reader()
        reader.values['Remote'] = True
        reader.values['Type'] = 'tty'
        result = observed(reader, 123, 1000, '')
        self.assertIs(result['Remote'], True)
        self.assertEqual(result['binding'], 'direct-session')
        self.assertEqual(reader.calls, [('GetSessionByPID', 123, False)])

    def test_sessionless_application_requires_actual_unit_cgroup_and_display_owner(self):
        reader = Reader(direct=False)
        result = observed(reader, 123, 1000, '0::' + GROUP + '\n')
        self.assertEqual(result['binding'], 'graphical-user-unit')
        self.assertIs(result['Active'], True)
        self.assertEqual(result['session_object'], SESSION)
        self.assertIn(('GetUnitByPID', 123, True), reader.calls)
        self.assertIn(('GetUserByPID', 123, False), reader.calls)
        for field, value in [('ControlGroup', GROUP + '-foreign'), ('UID', 1001),
                             ('UID', True), ('User', (1001, OWNER)), ('Remote', 0)]:
            reader = Reader(direct=False)
            reader.values[field] = value
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                observed(reader, 123, 1000, '0::' + GROUP + '\n')

    def test_only_exact_typed_no_session_error_selects_user_unit_observation(self):
        reader = Reader(direct=False)
        def refused(*args, **kwargs):
            raise CallFailure('org.freedesktop.DBus.Error.AccessDenied')
        reader.object = refused
        with self.assertRaises(CaseUnavailable):
            observed(reader, 123, 1000, '0::' + GROUP + '\n')
        reader = Reader(direct=False)
        reader.values['Display'] = ('', '/')
        with self.assertRaises(CaseUnavailable):
            observed(reader, 123, 1000, '0::' + GROUP + '\n')

    def test_user_scope_uses_its_concrete_cgroup_interface(self):
        reader = Reader(direct=False)
        reader.values['Id'] = 'owned.scope'
        self.assertEqual(observed(reader, 123, 1000, '0::' + GROUP + '\n')['binding'], 'graphical-user-unit')
        reader.values['Id'] = 'owned.slice'
        with self.assertRaises(CaseUnavailable):
            observed(reader, 123, 1000, '0::' + GROUP + '\n')

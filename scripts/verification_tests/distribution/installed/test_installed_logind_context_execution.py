"""Graphical execution is an actual session or an attested user-manager unit."""
import io
import unittest
from types import SimpleNamespace
from unittest.mock import patch

from linux_release.installed_cases import logind_context as owner

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
    def transport_reader(self, *, remote=LOGIN + '.NoSessionForPID'):
        class Error(Exception):
            pass
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
        values = {'Id': ('s', 'owned.service'), 'ControlGroup': ('s', GROUP),
            'UID': ('u', 1000), 'Display': ('(so)', ('1', SESSION)),
            'User': ('(uo)', (1000, OWNER)), 'Remote': ('b', False),
            'Active': ('b', True), 'Type': ('s', 'wayland')}
        closed = []
        def call(destination, path, interface, method, parameters, *args):
            if method == 'GetSessionByPID':
                raise Error()
            if method in {'GetUnitByPID', 'GetUserByPID'}:
                return Variant((UNIT if method == 'GetUnitByPID' else OWNER,))
            if method == 'Get':
                signature, value = values[parameters[1][1]]
                return Variant((Variant(value, signature),))
            raise AssertionError(method)
        reader = owner.Reader.__new__(owner.Reader)
        reader.closed = False
        reader.system = SimpleNamespace(call_sync=call, close_sync=lambda _: closed.append('system'))
        reader.user = SimpleNamespace(call_sync=call, close_sync=lambda _: closed.append('user'))
        reader.gio = SimpleNamespace(DBusCallFlags=SimpleNamespace(NONE=0),
            DBusError=SimpleNamespace(get_remote_error=lambda _: remote))
        reader.glib = SimpleNamespace(Error=Error, Variant=lambda *args: args,
            VariantType=SimpleNamespace(new=lambda signature: signature))
        return reader, closed

    def test_real_reader_preserves_typed_remote_refusal_for_graphical_unit_fallback(self):
        reader, closed = self.transport_reader()
        self.assertEqual(observed(reader, 123, 1000, '0::' + GROUP + '\n')['binding'], 'graphical-user-unit')
        self.assertFalse(reader.closed)
        self.assertEqual(closed, [])
        reader.close()
        self.assertEqual(closed, ['user', 'system'])

    def test_real_transport_failure_closes_all_private_connections(self):
        reader, closed = self.transport_reader(remote=None)
        with self.assertRaises(CaseUnavailable):
            observed(reader, 123, 1000, '')
        self.assertTrue(reader.closed)
        self.assertEqual(closed, ['user', 'system'])

    def test_failed_user_connection_closes_already_established_system_connection(self):
        reader, closed = self.transport_reader()
        reader.user = None
        with patch.object(reader, 'connect', side_effect=reader.glib.Error()):
            with self.assertRaises(CallFailure):
                reader.object('GetUnitByPID', 123, user=True)
        self.assertTrue(reader.closed)
        self.assertEqual(closed, ['system'])

    def test_execution_context_closes_its_owned_reader_on_observation_failure(self):
        closed = []
        class OwnedReader:
            def __enter__(self):
                return self
            def __exit__(self, *_):
                closed.append(True)
        with patch.object(owner.Path, 'stat', return_value=SimpleNamespace(st_size=0)), patch.object(
                owner.Path, 'open', return_value=io.StringIO('0::fixture\n')), patch.object(
                owner, 'Reader', return_value=OwnedReader()), patch.object(
                owner, 'observed', side_effect=ValueError('fixture observation failed')):
            with self.assertRaises(ValueError):
                owner.execution_session()
        self.assertEqual(closed, [True])

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

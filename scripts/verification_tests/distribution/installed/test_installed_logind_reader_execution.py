"""The logind reader speaks typed D-Bus and the execution session binds to a real cgroup."""
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

from linux_release.installed_cases import logind_context
from linux_release.installed_cases.records import CaseUnavailable


class Value:
    def __init__(self, signature, value):
        self.signature, self.value = signature, value

    def get_type_string(self):
        return self.signature

    def unpack(self):
        return self.value

    def get_child_value(self, _):
        return self

    def get_variant(self):
        return self


class GLibError(Exception):
    pass


class Connection:
    def __init__(self, name, replies, failures):
        self.name, self.replies, self.failures, self.calls = name, replies, failures, []

    def call_sync(self, destination, path, interface, method, parameters, reply_type, flags, timeout, cancellable):
        self.calls.append((destination, path, interface, method, parameters, reply_type, flags, timeout))
        if method in self.failures:
            raise GLibError(self.failures[method])
        return self.replies[method]


def fake_gi(replies=None, failures=None, system_error=False):
    """A gi.repository stand-in with the Gio and GLib surface the reader touches."""
    replies, failures = replies or {}, failures or {}
    connections = {}

    def bus_get_sync(kind, _):
        if kind == 'SYSTEM' and system_error:
            raise GLibError('no system bus')
        connections[kind] = connections.get(kind) or Connection(kind, replies, failures)
        return connections[kind]

    gio = types.SimpleNamespace(bus_get_sync=bus_get_sync, BusType=types.SimpleNamespace(SYSTEM='SYSTEM', SESSION='SESSION'),
                                DBusCallFlags=types.SimpleNamespace(NONE='none'),
                                DBusError=types.SimpleNamespace(get_remote_error=lambda error: str(error)))
    glib = types.SimpleNamespace(Variant=lambda signature, value: ('variant', signature, value), Error=GLibError,
                                 VariantType=types.SimpleNamespace(new=lambda signature: ('type', signature)))
    repository = types.ModuleType('gi.repository')
    repository.Gio, repository.GLib = gio, glib
    package = types.ModuleType('gi')
    package.repository = repository
    return patch.dict(sys.modules, {'gi': package, 'gi.repository': repository}), connections


class ReaderTests(unittest.TestCase):
    def test_missing_bindings_or_system_bus_leave_the_case_unverified(self):
        with patch.dict(sys.modules, {'gi': None, 'gi.repository': None}), self.assertRaises(CaseUnavailable):
            logind_context.Reader()
        modules, _ = fake_gi(system_error=True)
        with modules, self.assertRaises(CaseUnavailable):
            logind_context.Reader()

    def test_objects_and_properties_are_typed_calls_on_the_right_bus(self):
        replies = {'GetSessionByPID': Value('(o)', ('/org/freedesktop/login1/session/_3',)),
                   'GetUnitByPID': Value('(o)', ('/org/freedesktop/systemd1/unit/app',)),
                   'Get': Value('(v)', None)}
        replies['Get'] = Value('(v)', None)
        replies['Get'].get_child_value = lambda _: Value('s', 'app.service')
        modules, connections = fake_gi(replies)
        with modules:
            reader = logind_context.Reader()
            self.assertEqual(reader.object('GetSessionByPID', 42), '/org/freedesktop/login1/session/_3')
            self.assertEqual(reader.object('GetUnitByPID', 42, user=True), '/org/freedesktop/systemd1/unit/app')
            self.assertEqual(reader.property('/org/freedesktop/systemd1/unit/app', 'org.freedesktop.systemd1.Unit', 'Id', 's', user=True),
                             'app.service')
            with self.assertRaises(ValueError):
                reader.property('/org/freedesktop/systemd1/unit/app', 'org.freedesktop.systemd1.Unit', 'Id', 'u', user=True)
        system, session = connections['SYSTEM'], connections['SESSION']
        self.assertEqual(system.calls[0][:4], ('org.freedesktop.login1', '/org/freedesktop/login1',
                                               'org.freedesktop.login1.Manager', 'GetSessionByPID'))
        self.assertEqual(system.calls[0][4:], (('variant', '(u)', (42,)), ('type', '(o)'), 'none', 5000))
        self.assertEqual(session.calls[0][:4], ('org.freedesktop.systemd1', '/org/freedesktop/systemd1',
                                                'org.freedesktop.systemd1.Manager', 'GetUnitByPID'))
        self.assertEqual(session.calls[1][2:5], ('org.freedesktop.DBus.Properties', 'Get',
                                                 ('variant', '(ss)', ('org.freedesktop.systemd1.Unit', 'Id'))))
        self.assertEqual(len(session.calls), 3)

    def test_remote_errors_become_named_call_failures(self):
        modules, _ = fake_gi(failures={'GetSessionByPID': 'org.freedesktop.login1.NoSessionForPID'})
        with modules:
            reader = logind_context.Reader()
            with self.assertRaises(logind_context.CallFailure) as caught:
                reader.object('GetSessionByPID', 42)
        self.assertEqual(caught.exception.name, 'org.freedesktop.login1.NoSessionForPID')


class FakeReader:
    def __init__(self, objects, properties):
        self.objects, self.properties = objects, properties

    def object(self, method, pid, *, user=False):
        value = self.objects[method]
        if isinstance(value, Exception):
            raise value
        return value

    def property(self, path, interface, name, signature, *, user=False):
        return self.properties[name]


class ObservedBindingTests(unittest.TestCase):
    def test_invalid_unit_owner_and_session_objects_are_refused(self):
        no_session = logind_context.CallFailure('org.freedesktop.login1.NoSessionForPID')
        cases = [({'GetSessionByPID': no_session, 'GetUnitByPID': '/org/freedesktop/other/app'}, {}),
                 ({'GetSessionByPID': no_session, 'GetUnitByPID': '/org/freedesktop/systemd1/unit/app',
                   'GetUserByPID': '/org/freedesktop/login1/seat/seat0'},
                  {'Id': 'app.service', 'ControlGroup': '/user.slice/app.service'}),
                 ({'GetSessionByPID': '/org/freedesktop/login1/seat/seat0'}, {})]
        for objects, properties in cases:
            with self.subTest(objects=objects), self.assertRaises(ValueError):
                logind_context.observed(FakeReader(objects, properties), 42, 1000, '0::/user.slice/app.service\n')


class ExecutionSessionTests(unittest.TestCase):
    def cgroup(self, root: Path, content: str):
        path = root / 'cgroup'
        path.write_text(content)
        return patch.object(logind_context, 'Path', lambda value: path if value == '/proc/self/cgroup' else Path(value))

    def test_reads_the_bounded_cgroup_and_binds_the_current_process(self):
        with tempfile.TemporaryDirectory() as temporary:
            with self.cgroup(Path(temporary), '0::/user.slice/app.service\n'), \
                    patch.object(logind_context, 'Reader', return_value='reader'), \
                    patch.object(logind_context, 'observed', return_value={'binding': 'direct-session'}) as observed, \
                    patch.object(logind_context.os, 'getpid', return_value=42), \
                    patch.object(logind_context.os, 'getuid', return_value=1000):
                self.assertEqual(logind_context.execution_session(), {'binding': 'direct-session'})
            observed.assert_called_once_with('reader', 42, 1000, '0::/user.slice/app.service\n')

    def test_oversized_cgroup_files_and_call_failures_are_typed_outcomes(self):
        with tempfile.TemporaryDirectory() as temporary:
            with self.cgroup(Path(temporary), '0' * 65537), patch.object(logind_context, 'Reader') as reader, \
                    self.assertRaises(ValueError):
                logind_context.execution_session()
            reader.assert_not_called()
            failure = logind_context.CallFailure('org.freedesktop.DBus.Error.AccessDenied')
            with self.cgroup(Path(temporary), '0::/\n'), patch.object(logind_context, 'Reader', return_value='reader'), \
                    patch.object(logind_context, 'observed', side_effect=failure), self.assertRaises(CaseUnavailable) as caught:
                logind_context.execution_session()
            self.assertEqual(caught.exception.observations,
                             {'native_session_error': {'remote_name': 'org.freedesktop.DBus.Error.AccessDenied'}})


if __name__ == '__main__':
    unittest.main()

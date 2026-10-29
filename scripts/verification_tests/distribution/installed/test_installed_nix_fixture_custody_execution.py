"""The Nix fixture reads root-controlled inputs through held descriptors and loads one pinned derivative."""
import hashlib
import json
import os
from pathlib import Path
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.installed_cases import nix_fixture as fixture
from linux_release.installed_cases.records import CaseUnavailable

STORE = '/nix/store/' + 'a' * 32 + '-fixture-tools'
CLIENT = {'path': STORE + '/bin/nix', 'sha256': hashlib.sha256(b'client').hexdigest()}
DAEMON = {'path': STORE + '/bin/nix-daemon', 'sha256': hashlib.sha256(b'daemon').hexdigest()}
BUILDER = {'path': STORE + '/bin/busybox', 'sha256': hashlib.sha256(b'builder').hexdigest(), 'nar_hash': 'sha256-nar'}
TOKEN = 'c' * 32
DRV = '/nix/store/' + 'd' * 32 + '-paintedwolf-nix-cancel-' + TOKEN + '.drv'
OUTPUT = '/nix/store/' + 'e' * 32 + '-paintedwolf-nix-cancel-' + TOKEN


def record() -> dict:
    return {'format_version': 1, 'purpose': 'installed-nix-workflow-fixture', 'system': 'x86_64-linux',
            'client': dict(CLIENT), 'daemon': dict(DAEMON), 'static_builder': dict(BUILDER),
            'cancellation': {'derivation': DRV, 'derivation_sha256': 'f' * 64, 'output': OUTPUT, 'token': TOKEN,
                             'arguments': ['sh', '-c', fixture.cancellation_script(BUILDER['path'], TOKEN)]},
            'remote_builder': {}}


class RootFiles:
    """Serve named root-owned files from a scratch directory through the module's os surface."""

    def __init__(self, base: Path, files: dict, uid=0, mode=0o444, size_delta=0, mtime_change=False):
        self.base, self.files, self.uid, self.mode = base, files, uid, mode
        self.size_delta, self.mtime_change, self.reads = size_delta, mtime_change, 0
        for name, content in files.items():
            target = base / name.strip('/').replace('/', '_')
            target.write_bytes(content)

    def path(self, name):
        return self.base / str(name).strip('/').replace('/', '_')

    def open(self, name, flags):
        if str(name) not in self.files:
            raise FileNotFoundError(str(name))
        return os.open(self.path(name), flags)

    def fstat(self, descriptor):
        real = os.fstat(descriptor)
        self.reads += 1
        mtime = real.st_mtime_ns + (1 if self.mtime_change and self.reads % 2 == 0 else 0)
        return SimpleNamespace(st_mode=stat.S_IFREG | self.mode, st_uid=self.uid, st_size=real.st_size + self.size_delta,
                               st_dev=real.st_dev, st_ino=real.st_ino, st_mtime_ns=mtime)

    def __enter__(self):
        surface = SimpleNamespace(open=self.open, fdopen=os.fdopen, fstat=self.fstat, O_RDONLY=os.O_RDONLY,
                                  O_NOFOLLOW=os.O_NOFOLLOW, O_NONBLOCK=os.O_NONBLOCK)
        self.patch = patch.object(fixture, 'os', surface)
        self.patch.start()
        return self

    def __exit__(self, *_):
        self.patch.stop()


class MarkerTests(unittest.TestCase):
    def test_marker_is_read_through_a_held_root_owned_descriptor(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            with RootFiles(base, {str(fixture.MARKER): json.dumps(record()).encode()}):
                self.assertEqual(fixture.read_marker(), record())
            with RootFiles(base, {}), self.assertRaises(CaseUnavailable):
                fixture.read_marker()
            refusals = [dict(uid=1000), dict(mode=0o666), dict(size_delta=1), dict(mtime_change=True)]
            for changed in refusals:
                with self.subTest(**changed), RootFiles(base, {str(fixture.MARKER): b'{}'}, **changed), self.assertRaises(ValueError):
                    fixture.read_marker()

    def test_store_files_require_an_immutable_root_owned_image_with_the_declared_digest(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            with RootFiles(base, {CLIENT['path']: b'client'}):
                self.assertEqual(fixture.store_file(CLIENT, {'path', 'sha256'}), Path(CLIENT['path']))
                for value in [dict(CLIENT, extra=1), {'path': '/usr/bin/nix', 'sha256': CLIENT['sha256']},
                              dict(CLIENT, sha256='short'), dict(CLIENT, path=STORE + '/bin/evil')]:
                    with self.subTest(value=value), self.assertRaises(ValueError):
                        fixture.store_file(value, {'path', 'sha256'})
                with self.assertRaises(ValueError):
                    fixture.store_file(dict(CLIENT, sha256='0' * 64), {'path', 'sha256'})
            with RootFiles(base, {CLIENT['path']: b'client'}, mode=0o644), self.assertRaises(ValueError):
                fixture.store_file(CLIENT, {'path', 'sha256'})
            with RootFiles(base, {CLIENT['path']: b'client'}, mtime_change=True), self.assertRaises(ValueError):
                fixture.store_file(CLIENT, {'path', 'sha256'})


class DerivativeTests(unittest.TestCase):
    def test_undeclared_roles_and_invalid_derivative_identities_are_refused(self):
        with self.assertRaises(ValueError):
            fixture.derivation({}, record(), 'builder')
        for changed in [dict(token='short'), dict(derivation='/tmp/x.drv'), dict(output='/tmp/out'), dict(derivation=OUTPUT)]:
            selected = record()
            selected['cancellation'].update(changed)
            with self.subTest(**changed), self.assertRaises(ValueError):
                fixture.derivation({}, selected)

    def test_realized_outputs_are_typed_store_queries(self):
        with patch.object(fixture, 'nix_command', return_value=json.dumps({OUTPUT: {'narHash': 'sha256-x'}})) as command:
            self.assertTrue(fixture.realized(Path(CLIENT['path']), OUTPUT))
        self.assertEqual(command.call_args.args, (Path(CLIENT['path']), 'path-info', '--json', '--json-format', '1', OUTPUT))
        with patch.object(fixture, 'nix_command', return_value=json.dumps({OUTPUT: None})):
            self.assertFalse(fixture.realized(Path(CLIENT['path']), OUTPUT))


class LoadTests(unittest.TestCase):
    def binding(self, client=CLIENT, architecture='amd64'):
        return {'artifact': {'architecture': architecture}, 'payload': {'nix_client': dict(client)}}

    def load(self, marker, binding=None, nar='sha256-nar'):
        queries = {('path-info', STORE): json.dumps({STORE: {'narHash': nar}}),
                   ('derivation', DRV): json.dumps({'version': 4})}

        def nix_command(program, *arguments):
            return queries[(arguments[0], arguments[-1])]
        with patch.object(fixture, 'read_marker', return_value=marker), \
                patch.object(fixture, 'store_file', side_effect=lambda value, fields: Path(value['path'])) as files, \
                patch.object(fixture, 'nix_command', side_effect=nix_command), \
                patch.object(fixture, 'derivation', return_value=marker.get('cancellation')) as derivative:
            result = fixture.load(self.binding() if binding is None else binding)
        return result, files, derivative

    def test_load_binds_marker_store_files_builder_nar_and_the_installed_client(self):
        result, files, derivative = self.load(record())
        self.assertEqual(result, record())
        self.assertEqual([call.args for call in files.call_args_list],
                         [(CLIENT, {'path', 'sha256'}), (DAEMON, {'path', 'sha256'}), (BUILDER, {'path', 'sha256', 'nar_hash'}),
                          ({'path': DRV, 'sha256': 'f' * 64}, {'path', 'sha256'})])
        derivative.assert_called_once_with({'version': 4}, record())

    def test_load_refuses_every_mismatch_between_marker_store_and_binding(self):
        refusals = [(dict(record(), format_version=True), None, 'sha256-nar'), (dict(record(), purpose='other'), None, 'sha256-nar'),
                    (record(), self.binding(architecture='arm64'), 'sha256-nar'),
                    ({key: value for key, value in record().items() if key != 'daemon'}, None, 'sha256-nar'),
                    (record(), None, 'sha256-other'), (record(), self.binding(client=dict(CLIENT, sha256='0' * 64)), 'sha256-nar'),
                    (dict(record(), cancellation='none'), None, 'sha256-nar')]
        for index, (marker, binding, nar) in enumerate(refusals):
            with self.subTest(index=index), self.assertRaises((ValueError, KeyError)):
                self.load(marker, binding, nar)


class ObserverTests(unittest.TestCase):
    def test_observer_runs_the_fixed_script_as_root_and_returns_owned_and_live_rows(self):
        facts = {'owned': [{'pid': 7}], 'live': []}
        with patch.object(fixture.subprocess, 'run', return_value=SimpleNamespace(returncode=0, stdout=json.dumps(facts).encode())) as run:
            self.assertEqual(fixture.observe(record(), 'state'), facts)
        arguments = run.call_args.args[0]
        self.assertEqual(arguments[:2], ['sudo', '-n'])
        self.assertEqual(arguments[3:], ['-c', fixture.OBSERVER, 'state', TOKEN])
        self.assertEqual(run.call_args.kwargs['env'], {'PATH': '/usr/bin:/bin', 'LC_ALL': 'C'})
        with patch.object(fixture.subprocess, 'run') as run, self.assertRaises(ValueError):
            fixture.observe(record(), 'kill')
        run.assert_not_called()
        with patch.object(fixture.subprocess, 'run', return_value=SimpleNamespace(returncode=1, stdout=b'')), \
                self.assertRaises(CaseUnavailable):
            fixture.observe(record(), 'observe')
        for raw in [b'[]', json.dumps({'owned': [], 'extra': []}).encode(), json.dumps({'owned': {}, 'live': []}).encode()]:
            with patch.object(fixture.subprocess, 'run', return_value=SimpleNamespace(returncode=0, stdout=raw)), \
                    self.assertRaises(ValueError):
                fixture.observe(record(), 'cleanup')


if __name__ == '__main__':
    unittest.main()

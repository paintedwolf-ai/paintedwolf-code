"""NixOS snapshot observations require controlled factory and actual generation facts."""
from copy import deepcopy
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.installed_cases import nix_factory as factory


class InstalledNixFactoryTests(unittest.TestCase):
    def setUp(self):
        self.snapshot = {'kind': 'nixos-revision', 'value': 'a' * 40, 'channel': 'nixos-26.05'}
        self.binding = {'source_commit': 'b' * 40, 'artifact': {'identity': {
            'kind': 'nix-closure', 'store_path': '/nix/store/' + '0' * 32 + '-painted-wolf-code'}}}
        self.marker = {'schema_version': 1, 'channel': 'nixos-26.05', 'nixpkgs_revision': 'a' * 40,
                       'application_commit': 'b' * 40,
                       'application_store_path': self.binding['artifact']['identity']['store_path']}

    def test_actual_revision_and_controlled_marker_bind_the_installed_application(self):
        with patch.object(factory, 'read_marker', return_value=(self.marker, 'c' * 64)), \
                patch.object(factory, 'revision', return_value='a' * 40):
            result = factory.observe(self.binding, self.snapshot)
        self.assertEqual(result['snapshot'], self.snapshot)
        self.assertEqual(result['application_store_path'], self.marker['application_store_path'])

    def test_channel_revision_source_output_and_extra_claims_refuse(self):
        for key, value in [('channel', 'nixos-unstable'), ('nixpkgs_revision', 'd' * 40),
                           ('application_commit', 'e' * 40), ('application_store_path', '/nix/store/other'),
                           ('schema_version', True), ('unexpected', 'value')]:
            marker = {**self.marker, key: value}
            with self.subTest(key=key), patch.object(factory, 'read_marker', return_value=(marker, 'c' * 64)), \
                    self.assertRaises(ValueError):
                factory.observe(self.binding, self.snapshot)
        with patch.object(factory, 'read_marker', return_value=(self.marker, 'c' * 64)), \
                patch.object(factory, 'revision', return_value='f' * 40), self.assertRaises(ValueError):
            factory.observe(self.binding, self.snapshot)

    def test_uncontrolled_marker_and_duplicate_fields_refuse(self):
        with tempfile.TemporaryDirectory() as temporary:
            marker = Path(temporary) / 'marker'
            marker.write_text(json.dumps(self.marker))
            with patch.object(factory, 'MARKER', marker), patch.object(factory.os, 'fstat', return_value=
                    SimpleNamespace(st_mode=0o100666, st_uid=1000, st_size=12)), self.assertRaises(ValueError):
                factory.read_marker()
            marker.unlink()
            marker.symlink_to(Path(temporary) / 'other')
            with patch.object(factory, 'MARKER', marker), self.assertRaises(ValueError):
                factory.read_marker()
        with self.assertRaises(ValueError):
            json.loads('{"schema_version":1,"schema_version":1}', object_pairs_hook=factory.pairs)


class InstalledNixFactoryObservationTests(unittest.TestCase):
    def root_stat(self, size, **changes):
        fields = {'st_mode': 0o100444, 'st_uid': 0, 'st_size': size, 'st_dev': 7, 'st_ino': 11,
                  'st_mtime_ns': 1000, 'st_ctime_ns': 1000}
        return SimpleNamespace(**{**fields, **changes})

    def test_controlled_marker_is_read_once_and_digested(self):
        marker = {'schema_version': 1, 'channel': 'nixos-26.05', 'nixpkgs_revision': 'a' * 40,
                  'application_commit': 'b' * 40, 'application_store_path': '/nix/store/' + '0' * 32 + '-painted-wolf-code'}
        raw = json.dumps(marker).encode()
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary).resolve() / 'marker'
            path.write_bytes(raw)
            with patch.object(factory, 'MARKER', path), patch.object(factory.os, 'fstat', return_value=self.root_stat(len(raw))):
                self.assertEqual(factory.read_marker(), (marker, factory.hashlib.sha256(raw).hexdigest()))
            changed = [self.root_stat(len(raw)), self.root_stat(len(raw), st_mtime_ns=2000)]
            with patch.object(factory, 'MARKER', path), patch.object(factory.os, 'fstat', side_effect=changed), \
                    self.assertRaises(ValueError):
                factory.read_marker()
            with patch.object(factory, 'MARKER', path.with_name('absent')), self.assertRaises(factory.CaseUnavailable):
                factory.read_marker()

    def observer(self, mode=0o100555, uid=0, present=True):
        physical = SimpleNamespace(is_relative_to=lambda root: root == '/nix/store')
        information = SimpleNamespace(st_mode=mode, st_uid=uid)

        def stat():
            if not present:
                raise FileNotFoundError('nixos-version')
            return information
        physical.stat = stat
        return SimpleNamespace(resolve=lambda: physical)

    def test_running_revision_comes_from_the_immutable_observer(self):
        completed = SimpleNamespace(returncode=0, stdout=(('c' * 40) + '\n').encode())
        with patch.object(factory, 'VERSION', self.observer()), patch.object(factory.subprocess, 'run', return_value=completed) as run:
            self.assertEqual(factory.revision(), 'c' * 40)
        self.assertEqual(run.call_args.args[0][1], '--revision')
        self.assertEqual(run.call_args.kwargs['env'], {'PATH': '/run/current-system/sw/bin', 'LC_ALL': 'C'})
        with patch.object(factory, 'VERSION', self.observer(present=False)), self.assertRaises(factory.CaseUnavailable):
            factory.revision()
        for observer in (self.observer(mode=0o100666), self.observer(uid=1000), self.observer(mode=0o100444)):
            with patch.object(factory, 'VERSION', observer), self.assertRaises(ValueError):
                factory.revision()
        for completed in (SimpleNamespace(returncode=1, stdout=b''), SimpleNamespace(returncode=0, stdout=b'x' * 129)):
            with patch.object(factory, 'VERSION', self.observer()), patch.object(factory.subprocess, 'run', return_value=completed), \
                    self.assertRaises(factory.CaseUnavailable):
                factory.revision()
        with patch.object(factory, 'VERSION', self.observer()), \
                patch.object(factory.subprocess, 'run', return_value=SimpleNamespace(returncode=0, stdout=b'not-a-revision')), \
                self.assertRaises(ValueError):
            factory.revision()

    def test_observation_requires_an_independent_snapshot(self):
        binding = {'source_commit': 'b' * 40, 'artifact': {'identity': {'kind': 'nix-closure', 'store_path': '/nix/store/x'}}}
        for snapshot in ({'kind': 'nixos-revision', 'value': 'a' * 40}, {'kind': 'image', 'value': 'a' * 40, 'channel': 'nixos-26.05'},
                         {'kind': 'nixos-revision', 'value': 'A' * 40, 'channel': 'nixos-26.05'},
                         {'kind': 'nixos-revision', 'value': 'a' * 40, 'channel': 'nixos-25.11'}, 'nixos-26.05'):
            with self.subTest(snapshot=snapshot), self.assertRaises(ValueError):
                factory.observe(binding, snapshot)

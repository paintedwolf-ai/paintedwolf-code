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

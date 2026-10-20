"""Actual monitor and independently selected release facts bind installed variants."""
import json
import unittest
from unittest.mock import patch

from linux_release import installed_variants as owner
from linux_release.installed_cases.records import CaseUnavailable


class InstalledVariantTests(unittest.TestCase):
    def test_mutter_typed_logical_scales_and_physical_binding(self):
        state = {'type': 'ua((ssss)a(siiddada{sv})a{sv})a(iiduba(ssss)a{sv})a{sv}',
                 'data': [1, [], [[0, 0, 1.0, 0, True, [['Virtual-1', 'QEMU', 'Monitor', '1']], {}],
                                  [1024, 0, 1.5, 0, False, [['Virtual-2', 'QEMU', 'Monitor', '2']], {}]], {}]}
        with patch.object(owner, 'query', return_value={'exit_code': 0, 'stdout': json.dumps(state)}):
            self.assertEqual(owner.gnome_monitors({'Desktop': 'GNOME'})['scales_percent'], [100, 150])
        state['data'][2][0][2] = True
        with patch.object(owner, 'query', return_value={'exit_code': 0, 'stdout': json.dumps(state)}):
            with self.assertRaises(ValueError):
                owner.gnome_monitors({'Desktop': 'GNOME'})
        with self.assertRaises(CaseUnavailable):
            owner.gnome_monitors({'Desktop': 'Plasma'})

    def test_snapshot_authority_refuses_wrong_channel_shape_and_boolean(self):
        variant = {'release': {'snapshot_kind': 'nixos-revision', 'channel': 'nixos-unstable'}}
        correct = {'kind': 'nixos-revision', 'value': 'a' * 40, 'channel': 'nixos-unstable'}
        self.assertEqual(owner.snapshot(correct, variant), correct)
        for value in ({**correct, 'channel': 'nixos-26.05'}, {**correct, 'value': True},
                      {**correct, 'unexpected': 1}):
            with self.assertRaises(ValueError):
                owner.snapshot(value, variant)


class InstalledEvidenceCapacityTests(unittest.TestCase):
    def test_case_stream_count_and_aggregate_bytes_refuse_before_creating_output(self):
        import tempfile
        from pathlib import Path
        from linux_release.installed_cases.records import retain, MAX_STREAMS
        binding = {'source_commit': 'a' * 40, 'artifact': {}}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            with self.assertRaises(ValueError):
                retain(binding, 'session', 'case', root, 'passed', {},
                       {'stream-' + chr(97 + index): b'' for index in range(MAX_STREAMS + 1)})
            self.assertFalse((root / 'session').exists())
            with self.assertRaises(ValueError):
                retain(binding, 'session', 'case', root, 'passed', {},
                       {'stream-' + chr(97 + index): b'a' * (8 << 20) for index in range(5)})
            self.assertFalse((root / 'session').exists())

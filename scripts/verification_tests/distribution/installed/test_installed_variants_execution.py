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


class InstalledVariantAggregationTests(unittest.TestCase):
    def test_missing_or_failed_required_variant_cannot_be_promoted_by_other_passes(self):
        import copy
        selected = {'environment': 'fedora', 'capability': 'windowing', 'cases': ['resize']}
        row = {'row_id': 'row', 'status': 'passed', 'environment_facts': {'session': 'actual'},
               'cases': [{'id': 'resize', 'status': 'passed', 'evidence': [{'path': 'first', 'sha256': 'a' * 64}]}]}
        with patch.object(owner, 'requirements', return_value=[{'id': 'floor'}, {'id': 'newer'}]):
            missing = owner.aggregate({}, {'row': [('floor', row)]}, {'row': selected})[0]
            self.assertEqual(missing['status'], 'unverified')
            self.assertEqual(missing['cases'][0]['evidence'], row['cases'][0]['evidence'])
            complete = owner.aggregate({}, {'row': [('floor', row), ('newer', row)]}, {'row': selected})[0]
            self.assertEqual(complete['status'], 'passed')
            failed = copy.deepcopy(row)
            failed['cases'][0]['status'] = 'failed'
            self.assertEqual(owner.aggregate({}, {'row': [('floor', row), ('newer', failed)]}, {'row': selected})[0]['status'], 'failed')
            with self.assertRaises(ValueError):
                owner.aggregate({}, {'row': [('floor', row), ('floor', row)]}, {'row': selected})


class InstalledPlanVariantTests(unittest.TestCase):
    def test_plan_rejects_legacy_ambiguous_or_repeated_variant_authority(self):
        import copy
        from linux_release import installed_campaign as campaign
        target = {'id': 'guest', 'host': '127.0.0.1', 'port': 1234, 'user': 'tester',
                  'identity_file': '/private/key', 'known_hosts': '/private/known', 'checkout': '/controller',
                  'runtime_bin_dirs': [], 'artifact_id': 'selected', 'environment_id': 'fedora',
                  'final_artifacts': '/final.json', 'file_root': '/packages', 'install_root': '/opt/product',
                  'output': '/fresh/output', 'nix_program': None, 'case_inputs': None,
                  'variant_id': 'floor-scale-100', 'snapshot': {'kind': 'package-inventory', 'value': 'f' * 64, 'channel': None}}
        plan = {'format_version': 2, 'source_commit': 'a' * 40, 'driver_tree': 'b' * 40, 'targets': [target]}
        row = {'environment': 'fedora', 'installation': 'rpm', 'architecture': 'amd64', 'capability': 'windowing'}
        variant = {'id': target['variant_id'], 'release': {'snapshot_kind': 'package-inventory', 'channel': None}}
        with patch.object(campaign.evidence, 'artifacts', return_value={'selected': {'installation': 'rpm', 'architecture': 'amd64'}}), \
                patch.object(campaign, 'limits', return_value={'targets': 2}), \
                patch.object(campaign, 'expand', return_value=[row]), \
                patch.object(campaign, 'requirements', return_value=[variant]):
            self.assertEqual(campaign.validate_plan(plan, {}, {})[:2], ('a' * 40, 'b' * 40))
            for mutate in (lambda value: value.update(format_version=1),
                           lambda value: value['targets'][0].update(variant_id='invented'),
                           lambda value: value['targets'][0]['snapshot'].update(channel='nixos-unstable'),
                           lambda value: value['targets'].append({**value['targets'][0], 'id': 'repeated'})):
                invalid = copy.deepcopy(plan)
                mutate(invalid)
                with self.assertRaises(ValueError):
                    campaign.validate_plan(invalid, {}, {})

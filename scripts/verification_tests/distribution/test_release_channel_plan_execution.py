"""One retained monotonic plan drives native assembly, publication and per-channel halt selection."""
from copy import deepcopy
import hashlib
import json
from pathlib import Path
import tempfile
import sys
import unittest
from unittest.mock import patch

from scripts.linux_release import release_repositories as native, repository_withdrawal
from scripts.linux_release.repository_objects import encoded
from scripts.linux_release.repository_operations import identity, transition
from scripts.release_distribution_contract import channels
from scripts.release_distribution_contract.channels import select
import release_distribution as distribution

SOURCE = 'a' * 40
VERSION = '1.2.3'
BASE = 'https://downloads.paintedwolf.dev'


class SharedChannelPlanTests(unittest.TestCase):
    def test_stable_verifies_preview_when_it_advances(self):
        for previous, expected in [(None, ["stable", "preview"]), ({"version": "1.0.0-rc.1"}, ["stable", "preview"]),
                                   ({"version": "1.0.0"}, ["stable", "preview"]), ({"version": "1.1.0-rc.1"}, ["stable"])]:
            with patch.object(distribution, "read_storage", side_effect=lambda key: previous if "/preview/" in key else None):
                self.assertEqual(select("stable", "1.0.0", 1, "a" * 40)["channels"], expected)
        with patch.object(distribution, "read_storage", return_value=None) as read:
            self.assertEqual(select("preview", "1.0.0-rc.1", 1, "a" * 40)["channels"], ["preview"])
            self.assertEqual(read.call_args.args[0], "updates/preview/key-1/latest.json")

    def test_preview_storage_failure_cannot_be_treated_as_first_publication(self):
        with patch.object(distribution, "read_storage", side_effect=ValueError("storage unavailable")) as read:
            with self.assertRaisesRegex(ValueError, "storage unavailable"):
                select("stable", "1.0.0", 2, "a" * 40)
            read.assert_called_once_with("release-metadata/1.0.0/channel-plan.json")

    def test_website_cli_confirms_only_selected_channels(self):
        from release_distribution_contract import confirmation
        for preview, expected in [('1.2.3-rc.1', ['stable', 'preview']), ('1.3.0-rc.1', ['stable'])]:
            controls = {'updates/preview/key-1/latest.json': {'version': preview}}
            selected = channels.select('stable', VERSION, 1, SOURCE, read=controls.get)
            with tempfile.TemporaryDirectory() as temporary:
                plan = Path(temporary) / 'plan.json'
                plan.write_text(json.dumps(selected))
                argv = ['release_distribution.py', 'website', '--channel', 'stable', '--version', VERSION,
                        '--manifest-sha256', 'b' * 64, '--distribution-sha256', 'c' * 64, '--plan', str(plan)]
                with patch.object(sys, 'argv', argv), patch.object(distribution, 'read_storage', side_effect=controls.get), \
                     patch.object(confirmation, 'wait_for_website') as confirm:
                    distribution.main()
                self.assertEqual([call.args[1]['channel'] for call in confirm.call_args_list], expected)
                self.assertTrue(all(call.args[1]['version'] == VERSION for call in confirm.call_args_list))
                self.assertEqual(controls['updates/preview/key-1/latest.json']['version'], preview)

    def test_retained_plan_refuses_drift_and_retries_its_own_verified_activation(self):
        controls = {'updates/stable/key-1/latest.json': {'version': '1.2.2'},
                    'updates/preview/key-1/latest.json': {'version': '1.2.3-rc.1'}}
        selected = channels.select('stable', VERSION, 1, SOURCE, read=controls.get)
        controls['release-metadata/' + VERSION + '/channel-plan.json'] = deepcopy(selected)
        self.assertTrue(channels.check(selected, read=controls.get))
        controls['updates/preview/key-1/latest.json'] = {'version': '1.3.0-rc.1'}
        with self.assertRaisesRegex(ValueError, 'changed after'):
            channels.check(selected, read=controls.get)
        with self.assertRaisesRegex(ValueError, 'changed after'):
            channels.select('stable', VERSION, 1, SOURCE, read=controls.get)
        manifest = {'version': VERSION, 'pub_date': 'immutable', 'platforms': {'darwin-aarch64': {'signature': 'selected'}}}
        controls['updates/releases/' + VERSION + '.json'] = manifest
        controls['updates/stable/key-1/latest.json'] = {**manifest, 'pub_date': 'activation'}
        controls['updates/preview/key-1/latest.json'] = {**manifest, 'pub_date': 'activation'}
        self.assertEqual(channels.select('stable', VERSION, 1, SOURCE, read=controls.get), selected)
        controls['updates/preview/key-1/latest.json']['platforms'] = {}
        with self.assertRaisesRegex(ValueError, 'changed after'):
            channels.check(selected, read=controls.get)
        for changed in ({'channels': ['stable']}, {'source_commit': 'b' * 40}, {'signing_generation': True}):
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                channels.validate({**selected, **changed}, SOURCE, VERSION, 'stable', 1)

    def test_selected_channels_alone_reach_actual_native_and_halt_owners(self):
        for owner in (repository_withdrawal.repository_validation, repository_withdrawal.pacman_validation):
            patched = patch.object(owner, 'validate_manifest', return_value=[])
            patched.start()
            self.addCleanup(patched.stop)
        cases = [('stable', VERSION, '1.2.3-rc.1', ['stable', 'preview']),
                 ('stable', VERSION, '1.3.0-rc.1', ['stable']),
                 ('preview', '1.2.4-rc.1', '1.2.3', ['preview'])]
        for primary, version, preview, expected in cases:
            with self.subTest(primary=primary, preview=preview), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                controls = {'updates/preview/key-1/latest.json': {'version': preview}}
                selected = channels.select(primary, version, 1, SOURCE, read=controls.get)
                self.assertEqual(selected['channels'], expected)
                artifacts = root / 'artifacts'
                artifacts.mkdir()
                for platform in ('linux-x86_64', 'linux-aarch64'):
                    for extension in ('deb', 'rpm', 'pkg.tar.zst'):
                        path = artifacts / f'painted-wolf-code_v{version}_{platform}.{extension}'
                        path.write_bytes(path.name.encode())
                        (artifacts / (path.name + '.sha256')).write_text(hashlib.sha256(path.read_bytes()).hexdigest())
                def assembled(family, manifest, inputs, destination, channel, catalog, private, **kwargs):
                    destination.mkdir()
                    return {**manifest, 'family': family, 'channel': channel, 'rehearsal': False,
                            'signing_generation': 1, 'key_fingerprint': 'A' * 40, 'inventory_sha256': 'b' * 64}
                def pacman(manifest, inputs, destination, channel, catalog, private, rehearsal=False):
                    return assembled('pacman', manifest, inputs, destination, channel, catalog, private)
                with patch.object(native, 'native_version', return_value='1.2.3-1'), \
                     patch.object(native.native_repositories, 'assemble', side_effect=assembled), \
                     patch.object(native.pacman_repository, 'assemble', side_effect=pacman):
                    native.assemble(set(native.FAMILIES), artifacts, root / 'generations', version, SOURCE,
                                    selected['channels'], root / 'private')
                storage, published = {}, []
                untouched = next((channel for channel in ('stable', 'preview') if channel not in expected), None)
                if untouched:
                    storage[f'linux/channels/deb/{untouched}/current.json'] = {'version': preview if untouched == 'preview' else '1.2.2'}
                before = deepcopy(storage)
                def retain(receipt, offer, directory, base_url, qualified, **configuration):
                    qualified(SOURCE)
                    storage[native.withdrawal_key(receipt)] = receipt
                def publisher(receipt, directory, catalog, prefix, base_url, previous, mode, operation, phase, *, authorize):
                    authorize({'manifest': receipt, 'catalog': catalog, 'storage_prefix': prefix,
                               'base_url': base_url, 'operation': operation, 'expected': previous})
                    storage['linux/generations/' + identity(receipt) + '/generation.json'] = receipt
                    record = transition(receipt, storage.get(native.channel_key(receipt)), previous, operation)
                    storage[native.channel_key(receipt)] = record
                    published.append((receipt['family'], receipt['channel'], receipt.get('architecture')))
                    return record
                def admit(source):
                    self.assertEqual(source, SOURCE)
                    channels.check(selected, read=controls.get)
                blocked = {**controls, 'release-metadata/' + version + '/withdrawn.json': {'version': version, 'withdrawn': True}}
                def halted_admission(source):
                    channels.check(selected, read=blocked.get)
                with self.assertRaisesRegex(ValueError, 'withdrawn release'):
                    native.publish([root / 'generations'], artifacts, version, SOURCE, selected['channels'], BASE, halted_admission,
                        read_current=lambda key, directory: storage.get(key), retain=retain,
                        publishers={family: publisher for family in native.FAMILIES})
                self.assertEqual((storage, published), (before, []))
                self.assertFalse(any('/withdrawn' in key for key in storage))
                native.publish([root / 'generations'], artifacts, version, SOURCE, selected['channels'], BASE, admit,
                    read_current=lambda key, directory: storage.get(key), retain=retain,
                    publishers={family: publisher for family in native.FAMILIES})
                from scripts.verification_tests.distribution.repository_fixture_support import halt_distribution
                prepared = repository_withdrawal.prepare(halt_distribution(version, None, channels=selected["channels"]), read=lambda key, directory: storage.get(key))
                rows = [row for row in prepared if row['action'] == 'withdraw']
                self.assertEqual({(row['family'], row['channel'], row['architecture']) for row in rows}, set(published))
                self.assertEqual({row['channel'] for row in rows}, set(expected))
                self.assertEqual(len(rows), 4 * len(expected))
                self.assertTrue(all(row['receipt']['packages'] == [] for row in rows))
                repository_withdrawal.apply(rows, BASE, fetch=lambda *args: None,
                    publishers={family: publisher for family in native.FAMILIES})
                for row in rows:
                    record = storage[native.channel_key(row['receipt'])]
                    self.assertEqual((record['operation'], record['generation'], record['packages']),
                                     ('withdraw', row['replacement'], []))
                for key, value in before.items():
                    self.assertEqual(storage[key], value)

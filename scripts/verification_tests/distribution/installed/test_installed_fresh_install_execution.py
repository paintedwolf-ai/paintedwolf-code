"""A launch cannot replace an observed fresh native installation transaction."""
import copy
from pathlib import Path
import tempfile
import unittest

from linux_release.installed_cases.desktop import installation_transaction
from linux_release.installed_cases.records import retain


class InstalledFreshInstallTests(unittest.TestCase):
    def prepared(self, root):
        binding = {'source_commit': 'a' * 40, 'artifact': {'id': 'native-deb-amd64'}}
        observed = {'operation': 'fresh-install', 'expected_absent': True, 'installed': True,
                    'recovery_required': False, 'output_limit': False, 'profile_absent': True}
        result = retain(binding, 'installation', 'preinstallation-transaction', root, 'passed', observed,
                        {'installer': b'bounded output'})
        binding['installation_transaction'] = {'format_version': 1, 'status': 'passed',
            'source_commit': binding['source_commit'], 'artifact_id': binding['artifact']['id'],
            'evidence': result['evidence'], 'observations': observed}
        return binding

    def test_only_complete_matching_retained_transaction_supplies_install_authority(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binding = self.prepared(root)
            self.assertEqual(installation_transaction(binding, root), binding['installation_transaction']['evidence'])
            for changes in ({'status': 'failed'}, {'source_commit': 'b' * 40}, {'artifact_id': 'other'},
                            {'format_version': True}, {'evidence': []}):
                changed = copy.deepcopy(binding)
                changed['installation_transaction'].update(changes)
                with self.assertRaises(ValueError):
                    installation_transaction(changed, root)
            missing = copy.deepcopy(binding)
            missing['installation_transaction']['evidence'] *= 2
            with self.assertRaises(ValueError):
                installation_transaction(missing, root)

    def test_tampered_journal_and_claimed_effects_are_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binding = self.prepared(root)
            changed = copy.deepcopy(binding)
            changed['installation_transaction']['observations']['installed'] = False
            with self.assertRaises(ValueError):
                installation_transaction(changed, root)
            path = root / 'installation/preinstallation-transaction/execution.json'
            path.write_text('{}')
            with self.assertRaises(ValueError):
                installation_transaction(binding, root)
        self.assertIsNone(installation_transaction({}, Path('/does-not-exist')))

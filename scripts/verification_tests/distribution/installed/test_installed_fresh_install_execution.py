"""A launch cannot replace an observed fresh native installation transaction."""
import copy
from pathlib import Path
import tempfile
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import desktop
from linux_release.installed_cases.records import CaseFailed, CaseUnavailable

from linux_release.installed_cases.desktop import installation_transaction
from linux_release.installed_cases.records import retain


class InstalledFreshInstallTests(unittest.TestCase):
    def prepared(self, root):
        binding = {'source_commit': 'a' * 40, 'artifact': {'id': 'native-deb-amd64'}}
        observed = {'operation': 'fresh-install', 'expected_absent': True, 'installed': True,
                    'recovery_required': False, 'output_limit': False, 'profile_absent': True, 'webview_profile_absent': True}
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

    def test_clean_install_consumes_the_immediate_first_launch_without_launching_again(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binding = self.prepared(root)
            binding['execution_nonce'] = 'c' * 32
            app = MagicMock()
            app.__enter__.return_value = app
            app.observations = {'capture_complete': True, 'output_limit': False}
            app.request.return_value = {'status': 200}
            app.streams.return_value = {'app-stdout': b'owned first launch'}
            interactive = {'visible': True, 'sensitive': True, 'defunct': False, 'normal_actionable_control': True}
            with patch.object(desktop, 'InstalledApp', return_value=app), \
                    patch.object(desktop, 'interactive_window', return_value=interactive):
                first = desktop.prepare_first_launch(binding, root)
            self.assertEqual(first['status'], 'passed')
            binding['first_launch_transaction'] = first
            with patch.object(desktop, 'InstalledApp', side_effect=AssertionError('must not launch again')):
                result = desktop.execute(binding, 'installation', 'clean-install-and-launch', root)
            self.assertEqual(result['status'], 'passed')
            self.assertTrue(any('first-launch-transaction/' in item['path'] for item in result['evidence']))
            binding['execution_nonce'] = 'd' * 32
            with self.assertRaises(ValueError):
                desktop.observed_journal(binding, root, first, 'first-launch-transaction', first_launch=True)

    def test_first_launch_failure_or_unavailable_ui_is_retained_without_later_reinterpretation(self):
        for error, status in ((CaseFailed('captured crash'), 'failed'), (CaseUnavailable('missing UI'), 'unverified')):
            with self.subTest(status=status), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                binding = self.prepared(root)
                binding['execution_nonce'] = 'c' * 32
                app = MagicMock()
                app.__enter__.side_effect = error
                app.observations = {'capture_complete': True, 'output_limit': False, 'exit_code': -11}
                app.streams.return_value = {'app-stderr': b'owned failure output'}
                with patch.object(desktop, 'InstalledApp', return_value=app):
                    first = desktop.prepare_first_launch(binding, root)
                binding['first_launch_transaction'] = first
                self.assertEqual(first['status'], status)
                with patch.object(desktop, 'InstalledApp', side_effect=AssertionError('must not retry first launch')):
                    result = desktop.execute(binding, 'installation', 'clean-install-and-launch', root)
                self.assertEqual(result['status'], status)

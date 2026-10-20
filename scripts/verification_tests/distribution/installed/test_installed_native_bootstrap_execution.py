"""Package-only preparation observes before removal and never claims first launch."""
from contextlib import nullcontext
import hashlib
import json
import os
import signal
from pathlib import Path
import tempfile
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.installed_cases import native_bootstrap as owner
from linux_release import hosted_bootstrap as supervisor
from linux_release.installed_cases.records import retain


class NativeBootstrapTests(unittest.TestCase):
    def fixture(self, root):
        execution = {'format_version': 1, 'execution_nonce': 'a' * 32, 'source_commit': 'b' * 40,
            'driver_tree': 'c' * 40, 'artifact_id': 'app', 'environment_id': 'ubuntu', 'variant_id': 'baseline'}
        artifact = {'id': 'app', 'installation': 'deb', 'identity': {'sha256': 'd' * 64}}
        binding = {'source_commit': 'b' * 40, 'artifact': artifact, 'images': {'/selected': {'device': 1, 'inode': 2}}}
        inventory = {'arguments': ['dpkg-query', '-W', '-f=${binary:Package}\t${Version}\n'],
            'exit_code': 0, 'stdout': 'dpkg\t1\npainted-wolf-code\t1\n', 'stderr': ''}
        return execution, artifact, binding, inventory

    def test_snapshot_precedes_owned_removal_and_absence_has_positive_controls(self):
        for failure in (None, 'remove', 'process', 'version', 'owner'):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                execution, artifact, binding, inventory = self.fixture(root)
                order = []
                def command(arguments):
                    if arguments[:3] == ['sudo', '-n', 'dpkg']:
                        order.append('remove')
                        return (1 if failure == 'remove' else 0), b'', b''
                    if arguments[:3] == ['sudo', '-n', '/usr/bin/python3']:
                        return 0, json.dumps({'format_version': 1, 'matching_processes': [123] if failure == 'process' else []}).encode(), b''
                    if arguments[0] == 'dpkg-deb' or '--showformat=${Version}' in arguments:
                        return 0, b'2.0' if failure == 'version' and '--showformat=${Version}' in arguments else b'1.0', b''
                    if '--showformat=${Package}' in arguments:
                        return (1 if failure == 'owner' else 0), b'painted-wolf-code' if arguments[-1] == 'painted-wolf-code' else b'dpkg', b''
                    return 1, b'', b''
                def observe(_):
                    order.append('snapshot')
                    return inventory
                with patch.object(owner, 'prepare_install', return_value={'status': 'passed'}), \
                        patch.object(owner, 'bind', return_value=binding), \
                        patch.object(owner, 'native_lock', return_value=nullcontext()), \
                        patch.object(owner, 'current_package', return_value=root / 'app'), \
                        patch.object(owner, 'inspect', return_value={}), \
                        patch.object(owner.Path, 'home', return_value=root / 'home'), \
                        patch.object(owner, 'package_inventory', side_effect=observe), \
                        patch.object(owner, 'command', side_effect=command):
                    report = owner.bootstrap({}, 'b' * 40, 'app', root, root / 'install', root / 'output', execution)
                self.assertEqual(order, [] if failure == 'owner' else ['snapshot'] if failure == 'version' else ['snapshot', 'remove'])
                self.assertFalse(report['gui_initialized'])
                self.assertFalse(report['pristine_operating_system'])
                self.assertFalse(report['qualification_authority'])
                self.assertEqual(report['status'], 'failed' if failure else 'passed')
                journal = json.loads((root / 'output/evidence/installation/bootstrap-removal/execution.json').read_text())
                self.assertEqual(journal['observations']['recovery_required'], bool(failure))
                if failure is None:
                    self.assertTrue(journal['observations']['selected_processes_absent'])
                    self.assertEqual(journal['observations']['database-control']['exit_code'], 0)
                    self.assertEqual(journal['observations']['absence']['exit_code'], 1)

    def test_changed_payload_after_inventory_is_never_purged(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            execution, artifact, binding, inventory = self.fixture(root)
            with patch.object(owner, 'prepare_install', return_value={'status': 'passed'}), \
                    patch.object(owner, 'bind', side_effect=[binding, ValueError('changed payload')]), \
                    patch.object(owner, 'native_lock', return_value=nullcontext()), \
                    patch.object(owner, 'current_package', return_value=root / 'app'), \
                    patch.object(owner, 'inspect', return_value={}), \
                    patch.object(owner, 'package_inventory', return_value=inventory), \
                    patch.object(owner, 'command', return_value=(0, b'painted-wolf-code', b'')) as command:
                report = owner.bootstrap({}, 'b' * 40, 'app', root, root / 'install', root / 'output', execution)
            self.assertEqual(report['status'], 'failed')
            self.assertEqual(command.call_count, 1)
            journal = json.loads((root / 'output/evidence/installation/bootstrap-removal/execution.json').read_text())
            self.assertTrue(journal['observations']['recovery_required'])

    def test_supervisor_captures_real_exit_and_bounds_output(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            kill = os.killpg
            def held_group(pid, number):
                self.assertIsNotNone(os.waitid(os.P_PID, pid, os.WEXITED | os.WNOHANG | os.WNOWAIT))
                self.assertEqual(number, signal.SIGKILL)
                return kill(pid, number)
            with patch.object(supervisor.os, 'killpg', side_effect=held_group):
                self.assertEqual(supervisor.captured_child([sys.executable, '-c', 'print("observed");raise SystemExit(7)'], root, root), 7)
            self.assertEqual((root / 'bootstrap-supervision.stdout').read_bytes(), b'observed\n')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            with self.assertRaises(ValueError):
                supervisor.captured_child([sys.executable, '-c', 'import sys;sys.stdout.write("x"*100000)'], root, root)
            self.assertEqual((root / 'bootstrap-supervision.stdout').stat().st_size, 65536)

    def test_supervision_refuses_nonroot_before_child_or_state(self):
        with patch.object(supervisor.os, 'getuid', return_value=1000), \
                patch.object(supervisor, 'captured_child') as child:
            with self.assertRaises(ValueError):
                supervisor.supervise(Path('/unused'), {}, None)
            child.assert_not_called()

    def test_process_observer_refuses_unprivileged_before_proc_inventory(self):
        with patch.object(owner.os, 'getuid', return_value=1000), \
                patch.object(owner.Path, 'iterdir') as inventory:
            with self.assertRaises(ValueError):
                exec(owner.PROCESS_OBSERVER, {})
            inventory.assert_not_called()

    def test_supervision_binds_actual_success_and_refuses_changed_request(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            execution, _, _, _ = self.fixture(root)
            (root / 'execution-request.json').write_text(json.dumps(execution))
            selected = {'controller_commit': 'd' * 40, 'controller_tree': 'c' * 40, 'source_commit': 'b' * 40}
            account = SimpleNamespace(pw_dir=str(root / 'home'), pw_name='paintedwolf-test')
            with patch.object(supervisor.os, 'getuid', return_value=0), \
                    patch.object(supervisor.os, 'geteuid', return_value=0), \
                    patch.object(supervisor, 'controller'), \
                    patch.object(supervisor, 'physical', return_value=('root-owned',)), \
                    patch.object(supervisor, 'captured_child', return_value=0) as child:
                receipt = supervisor.supervise(root, selected, account)
                self.assertEqual(supervisor.verify(root, selected, account, execution), receipt)
                child.assert_called_once_with(supervisor.invocation(root, account), root / 'home/controller', root)
                receipt['exit_code'] = 7
                (root / 'bootstrap-supervision.json').chmod(0o600)
                (root / 'bootstrap-supervision.json').write_text(json.dumps(receipt))
                with self.assertRaises(ValueError):
                    supervisor.verify(root, selected, account, execution)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'execution-request.json').write_text(json.dumps(execution))
            with patch.object(supervisor.os, 'getuid', return_value=0), \
                    patch.object(supervisor.os, 'geteuid', return_value=0), \
                    patch.object(supervisor, 'controller'), \
                    patch.object(supervisor, 'physical', side_effect=[('before',), ('changed',)]), \
                    patch.object(supervisor, 'captured_child', return_value=0):
                with self.assertRaises(ValueError):
                    supervisor.supervise(root, selected, account)
            self.assertFalse((root / 'bootstrap-supervision.json').exists())

    def test_failed_installation_never_attempts_unowned_removal(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            execution, _, _, _ = self.fixture(root)
            with patch.object(owner, 'prepare_install', return_value={'status': 'unverified'}), \
                    patch.object(owner, 'command') as command, patch.object(owner, 'bind') as bind:
                report = owner.bootstrap({}, 'b' * 40, 'app', root, root / 'install', root / 'output', execution)
            self.assertEqual(report['status'], 'unverified')
            command.assert_not_called()
            bind.assert_not_called()

    def test_receipt_requires_exact_preleaf_nonce_snapshot_and_real_journals(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            execution, artifact, binding, inventory = self.fixture(root)
            evidence = root / 'evidence'
            evidence.mkdir()
            observations = {'profile_absent': True, 'webview_profile_absent': True}
            installed = retain(binding, 'installation', 'preinstallation-transaction', evidence, 'passed', observations)
            installation = {'format_version': 1, 'status': 'passed', 'source_commit': 'b' * 40, 'artifact_id': 'app',
                'evidence': installed['evidence'], 'observations': observations}
            removal = retain(binding, 'installation', 'bootstrap-removal', evidence, 'passed', {
                **observations, 'selected_processes_absent': True, 'recovery_required': False, 'remaining_payload_paths': []})
            report = {'format_version': 1, 'execution': execution, 'status': 'passed', 'bootstrap_only': True,
                'gui_initialized': False, 'pristine_operating_system': False, 'installation': installation,
                'qualification_authority': False, 'publication_authority': False,
                'inventory': inventory, 'snapshot': {'kind': 'package-inventory', 'channel': None,
                    'value': hashlib.sha256(json.dumps(inventory, sort_keys=True).encode()).hexdigest()}, 'removal': removal}
            path = root / 'bootstrap.json'
            path.write_text(json.dumps(report))
            self.assertEqual(owner.verify(root, execution, artifact), report)
            with self.assertRaises(ValueError):
                owner.verify(root, {**execution, 'execution_nonce': 'e' * 32}, artifact)
            report['snapshot']['value'] = 'f' * 64
            path.write_text(json.dumps(report))
            with self.assertRaises(ValueError):
                owner.verify(root, execution, artifact)


if __name__ == '__main__':
    unittest.main()

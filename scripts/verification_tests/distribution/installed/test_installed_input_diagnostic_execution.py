"""Partial physical input diagnostics retain scope without campaign admission."""
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_input import diagnostic
from linux_release import installed_execution


class InputDiagnosticTests(unittest.TestCase):
    def fixture(self, root):
        target = {'id': 'diagnostic-fedora', 'host': '127.0.0.1', 'port': 1234, 'user': 'pwtest',
            'identity_file': '/private/key', 'known_hosts': '/private/known', 'checkout': '/controller',
            'runtime_bin_dirs': [], 'artifact_id': 'native-rpm-amd64.rpm',
            'environment_id': 'diagnostic-desktop', 'variant_id': 'diagnostic-native-input',
            'final_artifacts': '/inputs/final-artifacts.json', 'file_root': '/inputs/packages',
            'install_root': '/opt/painted-wolf-code', 'output': '/fresh/guest-output',
            'nix_program': None, 'case_inputs': None, 'snapshot': {},
            'input_adapter': {'format_version': 1, 'kind': 'qemu-fixed-input', 'lease': '/root/vm-lease', 'vm_id': 'owned'}}
        path = root / 'target.json'
        path.write_text(json.dumps(target))
        args = ['--target', str(path), '--candidate-inputs', '/inputs', '--candidate-run-id', '1',
            '--candidate-artifact-id', '2', '--candidate-controller-commit', 'a' * 40,
            '--source-commit', 'b' * 40, '--source-tree', 'c' * 40, '--driver-tree', 'd' * 40,
            '--output', str(root / 'output')]
        return target, path, args

    def test_only_fixed_diagnostic_managed_invocation_and_false_authority(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            target, _, args = self.fixture(root)
            context = Mock()
            context.__enter__ = Mock(return_value=SimpleNamespace(actor=SimpleNamespace(value={
                'ssh': {key: target[key] for key in ('host', 'port', 'user')}})))
            context.__exit__ = Mock(return_value=False)
            with patch.object(installed_execution, 'current_tree', return_value='d' * 40), \
                    patch.object(diagnostic, 'Bridge', return_value=context), \
                    patch.object(diagnostic, 'run_managed', return_value=201) as run:
                self.assertEqual(diagnostic.main(args), 2)
            command = run.call_args.args[1][-1]
            self.assertIn('--diagnose-first-launch --continue-installed', command)
            self.assertIn('--input-vm-id owned', command)
            self.assertNotIn('--run-cases', command)
            document = json.loads((root / 'output/diagnostic-controller.json').read_text())
            self.assertTrue(document['partial'])
            self.assertFalse(document['qualification_authority'])
            self.assertFalse(document['publication_authority'])

    def test_unselected_or_campaign_variant_cannot_start_physical_input(self):
        for change in ({'input_adapter': None}, {'environment_id': 'admitted-environment'}):
            with tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                target, path, args = self.fixture(root)
                path.write_text(json.dumps({**target, **change}))
                with patch.object(diagnostic, 'Bridge') as bridge, self.assertRaises(ValueError):
                    diagnostic.main(args)
                bridge.assert_not_called()
                self.assertFalse((root / 'output').exists())


if __name__ == '__main__':
    unittest.main()

"""Diagnostic startup cannot stamp partial artifacts as campaign/publication authority."""
import contextlib
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release import installed_execution, installed_runtime
from linux_release.installed_cases import desktop, native_workflows


class InstalledDiagnosticTests(unittest.TestCase):
    def arguments(self, inputs, output):
        return ['--candidate-inputs', str(inputs), '--artifact-id', 'native-rpm-amd64.rpm',
                '--candidate-run-id', '11', '--candidate-artifact-id', '12',
                '--candidate-controller-commit', 'd' * 40, '--source-commit', 'b' * 40,
                '--source-tree', 'c' * 40, '--driver-tree', 'a' * 40,
                '--execution-nonce', 'e' * 32, '--output', str(output)]

    def test_unprivileged_inputs_are_refused_before_installation(self):
        with tempfile.TemporaryDirectory() as directory:
            inputs = Path(directory).resolve() / 'inputs'
            inputs.mkdir()
            output = inputs.parent / 'diagnostic'
            with patch.object(installed_execution, 'current_tree', return_value='a' * 40), \
                    patch.object(native_workflows, 'prepare_install', side_effect=AssertionError('must refuse first')), \
                    contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as refusal:
                    desktop.diagnostic_main(self.arguments(inputs, output))
            self.assertEqual(refusal.exception.code, 2)
            self.assertFalse(output.exists())

    def test_successful_diagnostic_has_no_campaign_receipt_or_publication_authority(self):
        with tempfile.TemporaryDirectory() as directory:
            inputs = Path(directory).resolve() / 'inputs'
            (inputs / 'packages').mkdir(parents=True)
            artifact = {'id': 'native-rpm-amd64.rpm', 'installation': 'rpm', 'architecture': 'amd64',
                        'source_commit': 'b' * 40, 'identity': {'kind': 'file', 'sha256': 'f' * 64}}
            selected = {'format_version': 1, 'partial': True, 'qualification_authority': False,
                        'publication_authority': False, 'source': {'source_commit': 'b' * 40, 'source_tree': 'c' * 40},
                        'provenance': {'run_id': 11, 'artifact_id': 12, 'controller_commit': 'd' * 40}}
            (inputs / 'candidate-selection.json').write_text(json.dumps(selected))
            (inputs / 'final-artifacts.json').write_text(json.dumps(
                {'format_version': 1, 'source_commit': 'b' * 40, 'artifacts': [artifact]}))
            controls = {inputs, inputs / 'packages', inputs / 'candidate-selection.json', inputs / 'final-artifacts.json'}
            for control in controls:
                control.chmod(0o755 if control.is_dir() else 0o644)
            original = Path.lstat
            def staged_owner(path, *args, **kwargs):
                info = original(path, *args, **kwargs)
                if path in controls:
                    values = list(info)
                    values[4] = 0
                    return os.stat_result(values)
                return info
            output = inputs.parent / 'diagnostic'
            with patch.object(installed_execution, 'current_tree', return_value='a' * 40), \
                    patch.object(Path, 'lstat', staged_owner), \
                    patch.object(native_workflows, 'prepare_install', return_value={'status': 'passed'}) as installer, \
                    patch.object(installed_runtime, 'bind', return_value={'artifact': artifact}), \
                    patch.object(installed_execution, 'execute_case', side_effect=lambda binding, cap, case, root:
                        {'id': case, 'status': 'passed', 'evidence': []}) as cases, \
                    patch.object(desktop, 'prepare_first_launch', return_value={'status': 'passed'}) as fresh, \
                    patch.object(desktop, 'observe_startup', return_value={'status': 'passed'}) as continued:
                self.assertEqual(desktop.diagnostic_main(self.arguments(inputs, output)), 0)
                for field, value in [('partial', False), ('qualification_authority', True), ('publication_authority', True)]:
                    changed = dict(selected, **{field: value})
                    (inputs / 'candidate-selection.json').write_text(json.dumps(changed))
                    refused = inputs.parent / ('refused-' + field)
                    with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                        desktop.diagnostic_main(self.arguments(inputs, refused))
                    self.assertFalse(refused.exists())
                (inputs / 'candidate-selection.json').write_text(json.dumps(selected))
                continuation = inputs.parent / 'continuation'
                self.assertEqual(desktop.diagnostic_main(
                    self.arguments(inputs, continuation) + ['--continue-installed']), 0)
                resumed = json.loads((continuation / 'diagnostic.json').read_text())
                self.assertEqual(resumed['scope'], 'partial-native-onboarding-continuation')
                self.assertIsNone(resumed['first_launch'])
                self.assertIsNone(resumed['installation'])
                self.assertEqual(resumed['continued_startup']['status'], 'passed')
                self.assertIs(resumed['qualification_authority'], False)
                self.assertEqual(fresh.call_count, 1)
                self.assertEqual(continued.call_args.args[2], 'continued-startup')
                self.assertEqual(installer.call_count, 1)
                self.assertEqual([call.args[1:3] for call in cases.call_args_list],
                                 [('conversation', 'provider-onboarding'), ('files', 'edit-search-review')] * 2)
            result = json.loads((output / 'diagnostic.json').read_text())
            for field in ('complete_candidate', 'application_qualified', 'qualification_authority', 'publication_authority'):
                self.assertIs(result[field], False)
            self.assertFalse((output / 'campaign.json').exists())
            self.assertFalse((output / 'execution.json').exists())

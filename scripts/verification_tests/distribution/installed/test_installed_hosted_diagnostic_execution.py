"""Partial hosted startup has fresh custody and never complete-campaign authority."""
import copy
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release import hosted_installed_diagnostic as diagnostic
from linux_release import hosted_installed as runtime


class HostedDiagnosticTests(unittest.TestCase):
    def test_complete_producer_and_existing_installation_refuse_before_writes(self):
        with patch.object(runtime, 'context') as context:
            with self.assertRaises(ValueError):
                diagnostic.prepare({'partial': False}, Path('/controller'), Path('/candidate'), {})
            context.assert_not_called()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / 'owned'
            root.mkdir()
            with patch.object(runtime, 'context'), patch.object(runtime, 'ROOT', root), patch.object(
                    runtime, 'candidate_package') as candidate:
                with self.assertRaises(ValueError):
                    diagnostic.prepare({'partial': True}, Path('/controller'), Path('/candidate'), {})
                candidate.assert_not_called()
            self.assertEqual(list(root.iterdir()), [])

    def test_partial_retention_checks_nonce_identity_and_false_authority(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            root, home = base / 'root', base / 'home'
            root.mkdir()
            leaf = home / 'installed-observations'
            (leaf / 'evidence').mkdir(parents=True)
            selected = {'partial': True, 'controller_commit': 'a' * 40, 'controller_tree': 'b' * 40,
                        'source_commit': 'c' * 40, 'source_tree': 'd' * 40}
            provenance = {'run_id': 12, 'artifact_id': 34, 'controller_commit': 'e' * 40}
            contract = {'controller_commit': selected['controller_commit'], 'candidate_provenance': provenance,
                        'output': str(leaf), 'execution_nonce': 'f' * 32}
            report = {'format_version': 1, 'scope': 'partial-native-first-launch-diagnostic', 'source_commit': selected['source_commit'],
                      'source_tree': selected['source_tree'], 'driver_tree': selected['controller_tree'],
                      'candidate_provenance': provenance, 'complete_candidate': False, 'application_qualified': False,
                      'qualification_authority': False, 'publication_authority': False, 'status': 'unverified',
                      'first_launch': {'execution_nonce': contract['execution_nonce']}}
            for name, value in [('contract.json', contract), ('candidate-selection.json', {}), ('final-artifacts.json', {})]:
                (root / name).write_text(json.dumps(value))
            mutations = [lambda value: value.update(qualification_authority=True),
                         lambda value: value.update(application_qualified=0),
                         lambda value: value.update(source_tree='0' * 40),
                         lambda value: value['first_launch'].update(execution_nonce='0' * 32)]
            with patch.object(runtime, 'ROOT', root), patch.object(runtime, 'context', return_value=SimpleNamespace(pw_dir=str(home))):
                for index, mutate in enumerate(mutations):
                    changed = copy.deepcopy(report)
                    mutate(changed)
                    (leaf / 'diagnostic.json').write_text(json.dumps(changed))
                    output = base / ('refused-' + str(index))
                    with self.subTest(index=index), self.assertRaises(ValueError):
                        diagnostic.retain(selected, base / 'controller', {}, output)
                    self.assertFalse(output.exists())
                (leaf / 'diagnostic.json').write_text(json.dumps(report))
                output = base / 'retained'
                self.assertEqual(diagnostic.retain(selected, base / 'controller', {}, output), report)
                self.assertEqual(json.loads((output / 'diagnostic/diagnostic.json').read_text()), report)
                retained = json.loads((output / 'rehearsal.json').read_text())
                for key in ('application_qualified', 'physical_gpu_qualified', 'qualification_authority', 'publication_authority'):
                    self.assertIs(retained[key], False)

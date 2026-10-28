"""The installed campaign entry point reads bounded inputs and reports the campaign verdict."""
import contextlib
import io
import json
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release import installed_evidence as evidence
from linux_release.campaign import execution


class InstalledExecutionInputTests(unittest.TestCase):
    def test_inputs_must_be_bounded_regular_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaises(ValueError):
                execution.read(root / 'missing.json')
            with self.assertRaises(ValueError):
                execution.read(root)
            small = root / 'inputs.json'
            small.write_text('{"ok": true}', encoding='utf-8')
            self.assertEqual(execution.read(small), {'ok': True})
            large = root / 'cases.json'
            large.write_text('[]', encoding='utf-8')
            with large.open('r+b') as handle:
                handle.truncate((16 << 20) + 1)
            with self.assertRaises(ValueError):
                execution.read(large)

    def test_driver_tree_requires_a_committed_source(self):
        tree = 'b' * 40
        clean = [SimpleNamespace(stdout=tree + '\n'), SimpleNamespace(stdout='')]
        with patch.object(execution.subprocess, 'run', side_effect=clean) as run:
            self.assertEqual(execution.current_tree(Path('/repo')), evidence.source(tree))
        self.assertEqual(run.call_args_list[0].args[0][:3], ['git', '-C', '/repo'])
        self.assertIn('--porcelain', run.call_args_list[1].args[0])
        dirty = [SimpleNamespace(stdout=tree + '\n'), SimpleNamespace(stdout=' M scripts/linux_release/campaign/execution.py\n')]
        with patch.object(execution.subprocess, 'run', side_effect=dirty):
            with self.assertRaises(ValueError):
                execution.current_tree(Path('/repo'))


class InstalledExecutionEntryTests(unittest.TestCase):
    def arguments(self, root: Path, case_inputs: bool = False) -> list[str]:
        (root / 'catalog.json').write_text('{"families": {}}', encoding='utf-8')
        (root / 'final.json').write_text('{"artifacts": []}', encoding='utf-8')
        arguments = ['--catalog', str(root / 'catalog.json'), '--final-artifacts', str(root / 'final.json'),
                     '--source-commit', 'a' * 40, '--artifact-id', 'deb-x86_64', '--file-root', str(root / 'files'),
                     '--install-root', str(root / 'install'), '--environment-id', 'gnome-x86_64',
                     '--output', str(root / 'out'), '--driver-tree', 'c' * 40, '--execution-nonce', 'd' * 32,
                     '--variant-id', 'gnome-wayland', '--snapshot', '{"vm": "snap"}']
        if case_inputs:
            (root / 'cases.json').write_text('{"documents": {"sample": "x"}}', encoding='utf-8')
            arguments += ['--case-inputs', str(root / 'cases.json'), '--input-vm-id', 'vm-7']
        return arguments

    def run_main(self, root: Path, outcome, case_inputs: bool = False):
        errors = io.StringIO()
        with patch.object(execution, 'execute', side_effect=[outcome] if not isinstance(outcome, Exception) else outcome) as execute, \
                contextlib.redirect_stderr(errors):
            code = execution.main(self.arguments(root, case_inputs))
        return code, errors.getvalue(), execute

    def test_campaign_verdict_becomes_the_exit_status(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            code, errors, execute = self.run_main(root, {'results': [{'status': 'passed'}, {'status': 'passed'}]}, case_inputs=True)
            self.assertEqual(code, 0)
            self.assertIn('Installed case campaign: ' + str(root / 'out' / 'campaign.json'), errors)
            call = execute.call_args.args
            self.assertEqual((call[0], call[1], call[2], call[3]), ({'families': {}}, {'artifacts': []}, 'a' * 40, 'deb-x86_64'))
            self.assertEqual((call[4], call[5], call[6], call[7]), (root / 'files', root / 'install', 'gnome-x86_64', root / 'out'))
            self.assertEqual((call[8], call[9], call[10]), ('c' * 40, None, {'documents': {'sample': 'x'}}))
            self.assertEqual((call[11], call[12], call[13], call[14]), ('d' * 32, 'gnome-wayland', {'vm': 'snap'}, 'vm-7'))
            code, _, execute = self.run_main(root, {'results': [{'status': 'passed'}, {'status': 'failed'}]})
            self.assertEqual(code, 1)
            self.assertIsNone(execute.call_args.args[10])

    def test_refused_execution_reports_the_reason_without_a_campaign(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for error in (ValueError('installed artifact differs from the selected candidate'),
                          subprocess.TimeoutExpired(['guest'], 15), OSError('no guest')):
                code, errors, _ = self.run_main(root, error)
                self.assertEqual(code, 2)
                self.assertIn('Installed case execution refused: ', errors)
                self.assertNotIn('Installed case campaign', errors)

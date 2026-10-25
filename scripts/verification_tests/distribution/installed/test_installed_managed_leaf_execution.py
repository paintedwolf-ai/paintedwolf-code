"""A managed partial leaf retains selected evidence without granting whole-campaign acceptance."""
from contextlib import ExitStack
import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release import installed_campaign as owner


class ManagedLeafImportTests(unittest.TestCase):
    def invoke(self, root, code, campaign, *, receipt_error=None):
        target = {'id': 'guest', 'artifact_id': 'deb-amd64', 'environment_id': 'noble', 'variant_id': 'floor',
                  'snapshot': {}, 'input_adapter': None, 'case_selection': {'format_version': 1,
                      'cases': [{'capability': 'session', 'case': 'selected'}]},
                  'final_artifacts': '/selected/final.json', 'file_root': '/selected/packages',
                  'install_root': '/selected/app', 'output': '/fresh/leaf', 'nix_program': None, 'case_inputs': None}
        def receive(_archive, leaf):
            leaf.mkdir()
            (leaf / 'campaign.json').write_text(json.dumps(campaign))
        with ExitStack() as stack:
            stack.enter_context(patch.object(owner, 'command', return_value=['captured-guest-command']))
            execution = stack.enter_context(patch.object(owner, 'run_managed', return_value=code))
            stack.enter_context(patch.object(owner.subprocess, 'run'))
            transferred = stack.enter_context(patch.object(owner, 'receive', side_effect=receive))
            receipt = stack.enter_context(patch.object(owner, 'verify_execution_receipt', side_effect=receipt_error))
            result = owner.run_guest(target, 'a' * 40, 'b' * 40, root)
        execution.assert_called_once()
        transferred.assert_called_once()
        receipt.assert_called_once()
        return result

    def partial(self, status='passed'):
        return {'results': [{'status': 'failed' if status == 'failed' else 'unverified', 'cases': [
            {'id': 'selected', 'status': status, 'evidence': [{'path': 'actual', 'sha256': 'a' * 64}]},
            {'id': 'not-selected', 'status': 'unverified', 'evidence': []}]}]}

    def test_task201_retains_selected_pass_and_omitted_cases_as_distinct_results(self):
        campaign = self.partial()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            leaf = self.invoke(root, 201, campaign)
            self.assertEqual(json.loads((leaf / 'campaign.json').read_text()), campaign)
            result = json.loads((root / 'guest/managed-result.json').read_text())
            self.assertEqual(result, {'format_version': 1, 'guest_managed_exit_code': 201,
                                     'complete_campaign_passed': False})

    def test_task201_never_changes_a_selected_failure_to_pass_or_unverified(self):
        campaign = self.partial('failed')
        with tempfile.TemporaryDirectory() as temporary:
            leaf = self.invoke(Path(temporary), 201, campaign)
            self.assertEqual(json.loads((leaf / 'campaign.json').read_text())['results'][0]['cases'][0]['status'], 'failed')

    def test_exit_must_agree_with_retained_complete_campaign(self):
        complete = {'results': [{'status': 'passed', 'cases': []}]}
        for code, campaign in ((0, self.partial()), (201, complete), (1, complete), (201, {'results': []})):
            with self.subTest(code=code), tempfile.TemporaryDirectory() as temporary, self.assertRaises(ValueError):
                self.invoke(Path(temporary), code, copy.deepcopy(campaign))
        with tempfile.TemporaryDirectory() as temporary:
            self.invoke(Path(temporary), 0, complete)

    def test_generic_nonzero_or_stale_receipt_never_gets_a_managed_result(self):
        for code, error in ((2, None), (201, ValueError('stale selected execution'))):
            with self.subTest(code=code), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                with self.assertRaises(ValueError):
                    self.invoke(root, code, self.partial(), receipt_error=error)
                self.assertFalse((root / 'guest/managed-result.json').exists())

    def test_partial_case_results_cannot_waive_non_case_admission_authority(self):
        source = 'a' * 40
        row = {'source_commit': source, 'tier': 'native-security', 'execution': 'native'}
        selected = {'tier': 'native-security', 'installation': 'deb', 'architecture': 'amd64', 'emulation_allowed': False}
        artifact = {'installation': 'deb', 'architecture': 'amd64'}
        owner.require_leaf_authority(row, selected, artifact, source)
        for changed in ({**row, 'source_commit': 'b' * 40}, {**row, 'tier': 'installed-functional'},
                        {**row, 'execution': 'emulated'}, {**row, 'execution': 'invented'}):
            with self.subTest(row=changed), self.assertRaisesRegex(ValueError, 'authority'):
                owner.require_leaf_authority(changed, selected, artifact, source)
        for changed in ({**artifact, 'architecture': 'arm64'}, {**artifact, 'installation': 'rpm'}):
            with self.subTest(artifact=changed), self.assertRaisesRegex(ValueError, 'authority'):
                owner.require_leaf_authority(row, selected, changed, source)

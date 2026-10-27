"""Completed guest evidence survives wrapper failure without becoming a pass."""
from contextlib import ExitStack
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release.campaign import guests as owner

SOURCE, TREE = 'b' * 40, 'c' * 40
RUN_GUEST = owner.run_guest


class GuestExitRetentionTests(unittest.TestCase):
    def run_guest(self, root, *, exit_code=201, status='unverified', receipt_change=None, incomplete=False):
        guest = root / 'guest'
        target = {'id': 'owned', 'artifact_id': 'selected', 'environment_id': 'guest',
            'variant_id': 'floor-scale-100', 'snapshot': {
                'kind': 'package-inventory', 'value': 'f' * 64, 'channel': None},
            'output': str(guest), 'final_artifacts': '/final', 'file_root': '/files',
            'install_root': '/installed', 'nix_program': None, 'case_inputs': None,
            'input_adapter': None}

        def command(selected, arguments):
            self.assertIs(selected, target)
            if arguments[0] == './task':
                nonce = arguments[arguments.index('--execution-nonce') + 1]
                request = {'format_version': 1, 'execution_nonce': nonce, 'source_commit': SOURCE,
                    'driver_tree': TREE, 'artifact_id': 'selected', 'environment_id': 'guest',
                    'variant_id': target['variant_id'], **(receipt_change or {})}
                variant = {'format_version': 1, 'variant': {'id': target['variant_id'],
                    'release': {'snapshot_kind': 'package-inventory', 'channel': None}},
                    'snapshot': target['snapshot'], 'monitors': {}, 'factory': None}
                case = {'observed': True}
                files = {'binding.json': {}, 'driver-registry.json': {}, 'execution.json': request,
                    'variant.json': variant, 'environment-facts.json': {}, 'evidence/case.json': case,
                    'campaign.json': {'results': [{'row_id': 'row', 'artifact_id': 'selected',
                        'status': status, 'cases': [{'id': 'case', 'status': status, 'evidence': [{
                            'path': 'case.json', 'sha256': hashlib.sha256(json.dumps(case).encode()).hexdigest()}]}]}]}}
                if incomplete:
                    files.pop('execution.json')
                # The real child writes its leaf, then returns the wrapper status.
                script = 'import pathlib,json\np=pathlib.Path(' + repr(str(guest)) + ')\np.mkdir()\n'
                script += 'files=' + repr(files) + '\n'
                script += 'for name,value in files.items():\n (p/name).parent.mkdir(parents=True,exist_ok=True)\n (p/name).write_text(json.dumps(value))\n'
                script += ('import os\nos.kill(os.getpid(), ' + repr(-exit_code) + ')\n' if exit_code < 0
                           else 'raise SystemExit(' + repr(exit_code) + ')\n')
                return [sys.executable, '-c', script]
            self.assertEqual(arguments[:2], ['python3', '-m'])
            return [sys.executable, *arguments[1:]]

        with patch.object(owner, 'command', side_effect=command):
            leaf = RUN_GUEST(target, SOURCE, TREE, root)
        return leaf

    def test_real_wrapper_201_retains_completed_unverified_or_failed_leaf(self):
        for status in ('unverified', 'failed'):
            with self.subTest(status=status), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                leaf = self.run_guest(root, status=status)
                campaign = owner.read(leaf / 'campaign.json')
                result = owner.verify_managed_result(root / 'owned', campaign)
                self.assertEqual(result['exit_code'], 201)
                self.assertEqual(campaign['results'][0]['status'], status)
                self.assertEqual(result['execution'], owner.read(leaf / 'execution.json'))
                self.assertTrue((root / 'owned/managed-execution.log').is_file())

    def test_nonzero_exit_never_establishes_pass_even_with_complete_bound_leaf(self):
        for code in (1, 201, -15):
            with self.subTest(code=code), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                leaf = self.run_guest(root, exit_code=code, status='passed')
                self.assertEqual(owner.read(root / 'owned/managed-execution.json')['exit_code'], code)
                with self.assertRaisesRegex(ValueError, 'cannot establish a passing'):
                    owner.verify_managed_result(root / 'owned', owner.read(leaf / 'campaign.json'))
                self.assertTrue(leaf.is_dir())

    def test_missing_and_stale_execution_receipts_remain_refused_after_transfer(self):
        for changes in ({'incomplete': True}, {'receipt_change': {'execution_nonce': 'd' * 32}},
                        {'receipt_change': {'driver_tree': 'e' * 40}}):
            with self.subTest(changes=changes), tempfile.TemporaryDirectory() as temporary:
                with self.assertRaises((ValueError, subprocess.CalledProcessError)):
                    self.run_guest(Path(temporary), **changes)
                self.assertEqual(owner.read(Path(temporary) / 'owned/managed-execution.json')['exit_code'], 201)

    def test_zero_exit_and_exact_host_receipt_are_required_for_a_passing_leaf(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            leaf = self.run_guest(root, exit_code=0, status='passed')
            campaign = owner.read(leaf / 'campaign.json')
            result = owner.verify_managed_result(root / 'owned', campaign)
            self.assertEqual(result['exit_code'], 0)
            path = root / 'owned/managed-execution.json'
            for changed in ({**result, 'exit_code': True}, {**result, 'format_version': True},
                            {**result, 'exit_code': 999}, {**result, 'execution': {}},
                            {**result, 'extra': 'untrusted'}):
                path.write_text(json.dumps(changed))
                with self.assertRaises(ValueError):
                    owner.verify_managed_result(root / 'owned', campaign)
            path.write_text(json.dumps(result))
            with self.assertRaisesRegex(ValueError, 'no completed case rows'):
                owner.verify_managed_result(root / 'owned', {'results': []})

    def test_collector_retains_host_exit_with_unverified_case_evidence(self):
        with tempfile.TemporaryDirectory() as temporary, ExitStack() as stack:
            output = Path(temporary) / 'collected'
            target = {'id': 'owned', 'artifact_id': 'selected', 'environment_id': 'guest',
                      'variant_id': 'floor-scale-100'}
            known = {'id': 'row', 'environment': 'guest', 'capability': 'component'}
            stack.enter_context(patch.object(owner, 'validate_plan', return_value=(SOURCE, TREE, [target])))
            stack.enter_context(patch.object(owner, 'run_guest', side_effect=lambda *args: self.run_guest(output)))
            stack.enter_context(patch.object(owner, 'expand', return_value=[known]))
            stack.enter_context(patch.object(owner, 'requirements', return_value=[{'id': target['variant_id']}]))
            stack.enter_context(patch.object(owner, 'aggregate', side_effect=lambda catalog, variants, rows: [variants['row'][0][1]]))
            stack.enter_context(patch.object(owner, 'admit', return_value={'accepted': False}))
            report = owner.execute({}, {'artifacts': []}, {'installations': []}, output)
            self.assertFalse(report['accepted'])
            campaign = owner.read(output / 'campaign.json')
            row, = campaign['results']
            self.assertEqual(row['status'], 'unverified')
            managed, = [item for item in row['cases'][0]['evidence']
                        if item['path'] == 'owned/managed-execution.json']
            path = output / 'evidence' / managed['path']
            self.assertEqual(hashlib.sha256(path.read_bytes()).hexdigest(), managed['sha256'])
            self.assertEqual(owner.read(path)['exit_code'], 201)

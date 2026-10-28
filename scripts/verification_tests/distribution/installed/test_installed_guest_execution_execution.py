"""Guest execution runs one fixed leaf per selected target and retains its receipts in place."""
import copy
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from linux_release.campaign import guests

SOURCE, TREE = 'a' * 40, 'b' * 40
NONCE = 'c' * 32


def target(identifier='guest', adapter=None) -> dict:
    return {'id': identifier, 'host': '127.0.0.1', 'port': 1234, 'user': 'tester', 'identity_file': '/private/key',
            'known_hosts': '/private/known', 'checkout': '/controller', 'runtime_bin_dirs': ['/runtime/bin'],
            'artifact_id': 'selected', 'environment_id': 'fedora', 'final_artifacts': '/final.json',
            'file_root': '/packages', 'install_root': '/opt/product', 'output': '/fresh/output',
            'nix_program': None, 'case_inputs': '/inputs.json', 'variant_id': 'floor-scale-100',
            'snapshot': {'kind': 'package-inventory', 'value': 'f' * 64, 'channel': None}, 'input_adapter': adapter}


def leaf_tree(directory: Path, identifier: str, campaign: dict):
    leaf = directory / identifier / 'leaf'
    (leaf / 'evidence' / 'cases').mkdir(parents=True)
    (leaf / 'evidence' / 'cases' / 'record.json').write_text('{}')
    for name, value in [('campaign.json', campaign), ('environment-facts.json', {'facts': 1}),
                        ('variant.json', {'variant': 1}), ('execution.json', {'execution': 1})]:
        (leaf / name).write_text(json.dumps(value))
    (directory / identifier / 'execution-request.json').write_text(json.dumps({'request': 1}))
    (directory / identifier / 'managed-execution.json').write_text(
        json.dumps({'format_version': 1, 'execution': {'request': 1}, 'exit_code': 0}))
    return leaf


class RunGuestTests(unittest.TestCase):
    def run_guest(self, directory: Path, selected: dict, returncode=0, lease_ssh=None):
        commands = []

        def command(row, arguments):
            commands.append(arguments)
            return ['ssh', 'fixture', *arguments]

        def export(arguments, stdout, stderr, check):
            stdout.write(b'archive')
            return SimpleNamespace(returncode=0)
        bridge = MagicMock()
        bridge.actor.value = {'ssh': {'host': '127.0.0.1', 'port': 1234, 'user': 'tester'} if lease_ssh is None else lease_ssh}
        bridge_manager = MagicMock()
        bridge_manager.__enter__.return_value = bridge
        with patch.object(guests, 'command', side_effect=command), \
                patch.object(guests.secrets, 'token_hex', return_value=NONCE), \
                patch.object(guests, 'Bridge', return_value=bridge_manager) as bridge_type, \
                patch.object(guests, 'run_managed', return_value=returncode) as managed, \
                patch.object(guests.subprocess, 'run', side_effect=export) as run, \
                patch.object(guests, 'receive') as receive, \
                patch.object(guests, 'verify_execution_receipt') as verify:
            leaf = guests.run_guest(selected, SOURCE, TREE, directory)
        return leaf, commands, dict(bridge_type=bridge_type, managed=managed, run=run, receive=receive, verify=verify, bridge=bridge)

    def test_a_plain_ssh_guest_runs_the_fixed_leaf_and_exports_its_output(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            leaf, commands, mocks = self.run_guest(directory, target())
            self.assertEqual(leaf, directory / 'guest' / 'leaf')
            request = json.loads((directory / 'guest' / 'execution-request.json').read_text())
            self.assertEqual(request, {'format_version': 1, 'execution_nonce': NONCE, 'source_commit': SOURCE,
                                       'driver_tree': TREE, 'artifact_id': 'selected', 'environment_id': 'fedora',
                                       'variant_id': 'floor-scale-100'})
            execution, export = commands
            self.assertEqual(execution[:3], ['./task', 'test:linux:installed', '--'])
            self.assertIn('--execution-nonce', execution)
            self.assertEqual(execution[execution.index('--execution-nonce') + 1], NONCE)
            self.assertEqual(execution[execution.index('--case-inputs') + 1], '/inputs.json')
            self.assertNotIn('--nix-program', execution)
            self.assertNotIn('--input-vm-id', execution)
            self.assertEqual(export, ['python3', '-m', 'scripts.linux_release.campaign.transfer', '--export', '/fresh/output'])
            mocks['bridge_type'].assert_not_called()
            mocks['managed'].assert_called_once_with(None, ['ssh', 'fixture', *execution], directory / 'guest' / 'managed-execution.log')
            self.assertEqual((directory / 'guest' / 'leaf.zip').read_bytes(), b'archive')
            self.assertTrue((directory / 'guest' / 'transfer.log').exists())
            mocks['receive'].assert_called_once_with(directory / 'guest' / 'leaf.zip', leaf)
            mocks['verify'].assert_called_once_with(directory / 'guest', target(), SOURCE, TREE)

    def test_a_fixed_input_guest_runs_through_its_lease_bound_bridge(self):
        adapter = {'format_version': 1, 'kind': 'qemu-fixed-input', 'lease': '/run/lease', 'vm_id': 'vm-1'}
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            leaf, commands, mocks = self.run_guest(directory, target(adapter=adapter))
            mailbox, execution, _ = commands
            self.assertEqual(mailbox[:3], ['python3', '-m', 'scripts.linux_release.installed_input.mailbox'])
            self.assertEqual(mailbox[mailbox.index('--vm-id') + 1], 'vm-1')
            self.assertEqual(execution[execution.index('--input-vm-id') + 1], 'vm-1')
            self.assertEqual(mocks['bridge_type'].call_args.args[0], adapter)
            self.assertEqual(mocks['bridge_type'].call_args.args[2], ['ssh', 'fixture', *mailbox])
            self.assertEqual(mocks['managed'].call_args.args[0], mocks['bridge'])
            (directory / 'again').mkdir()
            with self.assertRaises(ValueError):
                self.run_guest(directory / 'again', target(adapter=adapter), lease_ssh={'host': '10.0.0.9', 'port': 22, 'user': 'tester'})

    def test_a_nonzero_wrapper_status_is_retained_and_an_invalid_one_is_refused_before_export(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            leaf, _, mocks = self.run_guest(directory, target(), returncode=2)
            self.assertEqual(leaf, directory / 'guest' / 'leaf')
            managed = json.loads((directory / 'guest' / 'managed-execution.json').read_text())
            request = json.loads((directory / 'guest' / 'execution-request.json').read_text())
            self.assertEqual(managed, {'format_version': 1, 'execution': request, 'exit_code': 2})
            self.assertEqual((directory / 'guest' / 'leaf.zip').read_bytes(), b'archive')
            mocks['verify'].assert_called_once()
            for invalid in (256, -256, True, None):
                (directory / str(invalid)).mkdir()
                with self.subTest(returncode=invalid), self.assertRaisesRegex(ValueError, 'invalid exit status'):
                    self.run_guest(directory / str(invalid), target(), returncode=invalid)
                self.assertFalse((directory / str(invalid) / 'guest' / 'managed-execution.json').exists())
                self.assertFalse((directory / str(invalid) / 'guest' / 'leaf.zip').exists())


class ReceiptTests(unittest.TestCase):
    def test_a_foreign_or_stale_request_and_a_drifted_variant_are_refused(self):
        request = {'format_version': 1, 'execution_nonce': NONCE, 'source_commit': SOURCE, 'driver_tree': TREE,
                   'artifact_id': 'selected', 'environment_id': 'fedora', 'variant_id': 'floor-scale-100'}
        variant = {'format_version': 1, 'variant': {'id': 'floor-scale-100'}, 'snapshot': target()['snapshot'],
                   'monitors': [], 'factory': {}}
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            (directory / 'leaf').mkdir()
            documents = {'execution-request.json': request, 'leaf/execution.json': request, 'leaf/variant.json': variant}
            for name, value in documents.items():
                (directory / name).write_text(json.dumps(value))
            with patch.object(guests, 'snapshot') as snapshot, patch.object(guests, 'verify_deliveries') as deliveries:
                self.assertEqual(guests.verify_execution_receipt(directory, target(), SOURCE, TREE), request)
                snapshot.assert_called_once_with(variant['snapshot'], variant['variant'])
                deliveries.assert_called_once_with(directory, request, None)
            for name, changed in [('execution-request.json', dict(request, execution_nonce='short')),
                                  ('execution-request.json', dict(request, format_version='1')),
                                  ('leaf/execution.json', dict(request, execution_nonce='d' * 32)),
                                  ('leaf/execution.json', 'stale'),
                                  ('leaf/variant.json', dict(variant, snapshot={'kind': 'other'})),
                                  ('leaf/variant.json', {**variant, 'unverified': True})]:
                for original, value in documents.items():
                    (directory / original).write_text(json.dumps(value))
                (directory / name).write_text(json.dumps(changed))
                with self.subTest(name=name, changed=changed), patch.object(guests, 'snapshot'), \
                        patch.object(guests, 'verify_deliveries'), self.assertRaises(ValueError):
                    guests.verify_execution_receipt(directory, target(), SOURCE, TREE)


class PlanTests(unittest.TestCase):
    def test_plans_need_bounded_targets_over_declared_artifacts(self):
        plan = {'format_version': guests.PLAN_FORMAT_VERSION, 'source_commit': SOURCE, 'driver_tree': TREE, 'targets': [target()]}
        row = {'environment': 'fedora', 'installation': 'rpm', 'architecture': 'amd64', 'capability': 'windowing'}
        with patch.object(guests.evidence, 'artifacts', return_value={'selected': {'installation': 'rpm', 'architecture': 'amd64'}}), \
                patch.object(guests, 'limits', return_value={'targets': 1}), \
                patch.object(guests, 'expand', return_value=[row]), \
                patch.object(guests, 'requirements', return_value=[{'id': 'floor-scale-100'}]), \
                patch.object(guests, 'snapshot'):
            self.assertEqual(guests.validate_plan(plan, {}, {})[2], [target()])
            for mutate in (lambda value: value.update(targets=[]),
                           lambda value: value.update(targets=[target(), target('second')]),
                           lambda value: value['targets'][0].update(artifact_id='undeclared')):
                invalid = copy.deepcopy(plan)
                mutate(invalid)
                with self.assertRaises(ValueError):
                    guests.validate_plan(invalid, {}, {})


class ExecuteTests(unittest.TestCase):
    def campaign(self, artifact='selected', row_id='fedora-rpm', case_status='passed'):
        return {'format_version': 1, 'results': [{'artifact_id': artifact, 'row_id': row_id, 'cases': [
            {'id': 'case', 'status': case_status, 'evidence': [{'path': 'cases/record.json', 'sha256': 'e' * 64}]}]}]}

    def execute(self, output: Path, campaign: dict, known=None, required=('floor-scale-100',)):
        catalog = {'installations': [{'id': 'rpm'}]}
        final = {'artifacts': [{'id': 'selected'}]}
        plan = {'format_version': guests.PLAN_FORMAT_VERSION, 'source_commit': SOURCE, 'driver_tree': TREE, 'targets': [target()]}
        known = [{'id': 'fedora-rpm', 'environment': 'fedora', 'capability': 'windowing'}] if known is None else known
        aggregated = []

        def aggregate(catalog_value, variants, known_rows):
            aggregated.append(variants)
            return [row for rows in variants.values() for _, row in rows]
        with patch.object(guests, 'validate_plan', return_value=(SOURCE, TREE, [target()])), \
                patch.object(guests, 'registry', return_value={'format_version': 1, 'cases': []}), \
                patch.object(guests, 'run_guest', side_effect=lambda row, source, tree, out: leaf_tree(out, row['id'], campaign)) as run, \
                patch.object(guests, 'admit', return_value={'accepted': True}) as admit, \
                patch.object(guests, 'expand', return_value=known), \
                patch.object(guests, 'requirements', return_value=[{'id': value} for value in required]), \
                patch.object(guests.evidence, 'retained_file') as retained, \
                patch.object(guests, 'aggregate', side_effect=aggregate):
            report = guests.execute(plan, final, catalog, output)
        return report, aggregated, run, admit, retained

    def test_each_target_leaf_is_admitted_copied_and_its_evidence_paths_are_namespaced(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / 'campaign'
            report, aggregated, run, admit, retained = self.execute(output, self.campaign())
            self.assertEqual(report, {'accepted': True})
            self.assertEqual(run.call_count, 1)
            self.assertEqual(admit.call_count, 2)
            retained.assert_called_once_with({'path': 'guest/cases/record.json', 'sha256': 'e' * 64}, output / 'guest' / 'leaf' / 'evidence')
            campaign = json.loads((output / 'campaign.json').read_text())
            self.assertEqual(campaign['source_commit'], SOURCE)
            self.assertEqual(campaign['artifacts'], [{'id': 'selected'}])
            evidence = campaign['results'][0]['cases'][0]['evidence']
            self.assertEqual([record['path'] for record in evidence],
                             ['guest/cases/record.json', 'guest/environment-facts.json', 'guest/variant.json',
                              'guest/execution.json', 'guest/execution-request.json', 'guest/managed-execution.json'])
            self.assertEqual(aggregated, [{'fedora-rpm': [('floor-scale-100', campaign['results'][0])]}])
            for name in ('cases/record.json', 'environment-facts.json', 'variant.json', 'execution.json',
                         'execution-request.json', 'managed-execution.json'):
                self.assertTrue((output / 'evidence' / 'guest' / name).is_file(), name)
            self.assertEqual(json.loads((output / 'admission.json').read_text()), {'accepted': True})
            self.assertEqual(json.loads((output / 'driver-registry.json').read_text())['installations'], {'rpm': {'format_version': 1, 'cases': []}})
            self.assertEqual(json.loads((output / 'plan.json').read_text())['targets'][0]['id'], 'guest')
            with self.assertRaises(FileExistsError):
                self.execute(output, self.campaign())

    def test_leaves_that_drift_from_the_selected_artifact_environment_or_variant_are_refused(self):
        refusals = [dict(campaign=self.campaign(artifact='other')),
                    dict(campaign=self.campaign(row_id='unknown')),
                    dict(campaign=self.campaign(), known=[{'id': 'fedora-rpm', 'environment': 'ubuntu', 'capability': 'windowing'}]),
                    dict(campaign=self.campaign(), required=('other-variant',))]
        for changed in refusals:
            with tempfile.TemporaryDirectory() as temporary, self.subTest(**changed), self.assertRaises(ValueError):
                self.execute(Path(temporary) / 'campaign', **changed)


class MainTests(unittest.TestCase):
    def test_the_entry_point_reports_acceptance_refusal_and_rejection(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            for name in ('plan.json', 'final.json', 'catalog.json'):
                (directory / name).write_text('{}')
            arguments = ['--plan', str(directory / 'plan.json'), '--final-artifacts', str(directory / 'final.json'),
                         '--catalog', str(directory / 'catalog.json'), '--output', str(directory / 'out')]
            for accepted, code in [(True, 0), (False, 1)]:
                with patch.object(guests, 'execute', return_value={'accepted': accepted}) as execute:
                    self.assertEqual(guests.main(arguments), code)
                execute.assert_called_once_with({}, {}, {}, directory / 'out')
            with patch.object(guests, 'execute', side_effect=ValueError('refused')):
                self.assertEqual(guests.main(arguments), 2)


if __name__ == '__main__':
    unittest.main()

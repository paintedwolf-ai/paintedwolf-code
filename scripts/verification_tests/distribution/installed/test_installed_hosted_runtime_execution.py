"""Hosted rehearsal selection and custody never confer release authority."""
import copy
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release import hosted_installed as runtime
from linux_release import hosted_bootstrap as supervisor
from linux_release import hosted_installed_plan as plan


def selected():
    return {'format_version': 1, 'controller_commit': 'a' * 40, 'controller_tree': 'b' * 40,
            'components': {'native_candidates': 'c' * 40, 'support_variants': 'd' * 40, 'installed_execution': 'e' * 40},
            'architecture': 'arm64', 'candidate_run_id': 12, 'candidate_artifact_id': 34,
            'candidate_controller_commit': 'f' * 40, 'source_commit': '1' * 40, 'source_tree': '2' * 40, 'partial': False}


class HostedSelectionTests(unittest.TestCase):
    def test_selection_requires_every_frozen_component_and_typed_run(self):
        value = selected()
        self.assertEqual(plan.selection(value), value)
        for mutate in (lambda item: item['components'].pop('native_candidates'),
                       lambda item: item.update(candidate_artifact_id=True),
                       lambda item: item.update(controller_commit='main'),
                       lambda item: item.update(partial='false'),
                       lambda item: item.update(extra='untrusted')):
            bad = copy.deepcopy(value)
            mutate(bad)
            with self.assertRaises(ValueError):
                plan.selection(bad)

    def test_checkout_verifies_exact_tree_clean_source_and_component_ancestry(self):
        results = [SimpleNamespace(stdout=value) for value in ('a' * 40, 'b' * 40, '', '', '', '')]
        with patch.object(plan.subprocess, 'run', side_effect=results) as git:
            plan.controller(Path('/controller'), selected())
        self.assertEqual(git.call_count, 6)
        self.assertEqual(git.call_args_list[-1].args[0][-4:], ['merge-base', '--is-ancestor', 'e' * 40, 'a' * 40])
        for values in (['0' * 40], ['a' * 40, '0' * 40], ['a' * 40, 'b' * 40, ' M scripts/linux_release/hosted_installed.py']):
            with patch.object(plan.subprocess, 'run', side_effect=[SimpleNamespace(stdout=value) for value in values]):
                with self.assertRaises(ValueError):
                    plan.controller(Path('/controller'), selected())


class HostedRuntimeCustodyTests(unittest.TestCase):
    def test_complete_preparation_freeze_and_retention_refuse_partial_before_effects(self):
        partial = {**selected(), 'partial': True}
        with patch.object(runtime, 'context') as context:
            for operation, arguments in ((runtime.prepare, (partial, Path('/controller'), Path('/candidate'), {})),
                    (runtime.freeze, (partial, Path('/controller'), {})),
                    (runtime.retain, (partial, Path('/controller'), {}, Path('/output')))):
                with self.subTest(operation=operation.__name__), self.assertRaises(ValueError):
                    operation(*arguments)
            context.assert_not_called()

    def test_snapshot_freeze_is_independent_preleaf_preparation_and_cannot_be_reused(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            home = root / 'home'
            original = home / 'native-bootstrap'
            original.mkdir(parents=True, mode=0o700)
            custody = root / 'custody'
            custody.mkdir()
            request = {'format_version': 1, 'execution_nonce': 'a' * 32, 'source_commit': '1' * 40,
                'driver_tree': 'b' * 40, 'artifact_id': 'app', 'environment_id': 'ubuntu', 'variant_id': 'baseline'}
            contract = {**request, 'controller_commit': 'a' * 40, 'bootstrap_frozen': False,
                'snapshot': None, 'bootstrap_output': str(original)}
            import json, os
            (custody / 'contract.json').write_text(json.dumps(contract))
            (custody / 'execution-request.json').write_text(json.dumps(request))
            (custody / 'final-artifacts.json').write_text('{}')
            snapshot = {'kind': 'package-inventory', 'value': 'd' * 64, 'channel': None}
            from linux_release.installed_cases import native_bootstrap
            with patch.object(runtime, 'ROOT', custody), \
                    patch.object(runtime, 'context', return_value=SimpleNamespace(pw_dir=str(home), pw_uid=os.getuid())), \
                    patch.object(runtime, 'artifacts', return_value={'app': {'id': 'app'}}), \
                    patch.object(runtime, 'retain_tree') as retain, \
                    patch.object(supervisor, 'verify', return_value={}), \
                    patch.object(native_bootstrap, 'verify', return_value={'snapshot': snapshot}) as verify:
                result = runtime.freeze(selected(), Path('/controller'), {})
                self.assertEqual(result['snapshot'], snapshot)
                self.assertTrue(result['bootstrap_frozen'])
                verify.assert_called_once_with(custody / 'bootstrap', request, {'id': 'app'})
                self.assertEqual(retain.call_args.args[:2], (original, custody / 'bootstrap'))
                with self.assertRaises(ValueError):
                    runtime.freeze(selected(), Path('/controller'), {})
                (custody / 'contract.json').write_text(json.dumps(contract))
                def mutate(*_):
                    (custody / 'contract.json').write_text(json.dumps({**contract, 'source_commit': 'f' * 40}))
                    return {'snapshot': snapshot}
                verify.side_effect = mutate
                with self.assertRaises(ValueError):
                    runtime.freeze(selected(), Path('/controller'), {})
                self.assertEqual(json.loads((custody / 'contract.json').read_text())['source_commit'], 'f' * 40)

    def test_existing_runtime_refuses_before_package_or_system_changes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / 'existing'
            root.mkdir()
            (root / 'owner').write_text('preserve')
            with patch.object(runtime, 'context'), patch.object(runtime, 'ROOT', root), patch.object(runtime, 'command') as command:
                with self.assertRaises(ValueError):
                    runtime.prepare(selected(), Path('/controller'), Path('/candidate'), {})
            command.assert_not_called()
            self.assertEqual((root / 'owner').read_text(), 'preserve')

    def test_mismatched_candidate_or_release_authority_refuses_before_installation(self):
        producer = {'source': {'source_commit': '1' * 40, 'source_tree': '2' * 40},
                    'partial': False, 'qualification_authority': False, 'publication_authority': False,
                    'provenance': {'run_id': 12, 'artifact_id': 34, 'controller_commit': 'f' * 40}}
        for changed in ({**producer, 'qualification_authority': True},
                        {**producer, 'source': {'source_commit': '0' * 40, 'source_tree': '2' * 40}},
                        {**producer, 'provenance': {**producer['provenance'], 'artifact_id': 35}}):
            with tempfile.TemporaryDirectory() as temporary, patch.object(runtime, 'context'), \
                    patch.object(runtime, 'ROOT', Path(temporary) / 'fresh'), \
                    patch.object(runtime, 'INSTALL', Path(temporary) / 'absent'), \
                    patch.object(runtime, 'read', side_effect=[{}, changed]), patch.object(runtime, 'command') as command:
                with self.assertRaises(ValueError):
                    runtime.prepare(selected(), Path('/controller'), Path('/candidate'), {})
                command.assert_not_called()

    def test_fixed_install_command_uses_clean_administrator_environment(self):
        with patch.object(runtime.subprocess, 'run', return_value=SimpleNamespace(returncode=0, stdout=b'done\n', stderr=b'')) as run:
            self.assertEqual(runtime.command(['/usr/bin/dpkg-deb', '--field', '/selected.deb', 'Package']), 'done')
        self.assertEqual(set(run.call_args.kwargs['env']), {'PATH', 'DEBIAN_FRONTEND', 'LC_ALL'})
        self.assertEqual(run.call_args.kwargs['env']['PATH'], '/usr/sbin:/usr/bin:/sbin:/bin')

"""Hosted rehearsal selection and custody never confer release authority."""
import copy
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.hosted import installed as runtime
from linux_release.hosted import bootstrap as supervisor
from linux_release.hosted import plan


def selected():
    return {'format_version': 1, 'controller_commit': 'a' * 40, 'controller_tree': 'b' * 40,
            'architecture': 'arm64', 'candidate_run_id': 12, 'candidate_artifact_id': 34,
            'candidate_controller_commit': 'f' * 40, 'source_commit': '1' * 40, 'source_tree': '2' * 40, 'partial': False}


class HostedSelectionTests(unittest.TestCase):
    def test_selection_requires_exact_typed_identities(self):
        value = selected()
        self.assertEqual(plan.selection(value), value)
        for mutate in (lambda item: item.update(candidate_artifact_id=True),
                       lambda item: item.update(controller_commit='main'),
                       lambda item: item.update(architecture='riscv64'),
                       lambda item: item.update(partial='false'),
                       lambda item: item.update(extra='untrusted')):
            bad = copy.deepcopy(value)
            mutate(bad)
            with self.assertRaises(ValueError):
                plan.selection(bad)

    def test_checkout_is_observed_clean_and_must_match_the_selection(self):
        clean = lambda: [SimpleNamespace(stdout=value) for value in ('', 'a' * 40, 'b' * 40)]
        with patch.object(plan.subprocess, 'run', side_effect=clean()):
            plan.controller(Path('/controller'), selected())
        with patch.object(plan.subprocess, 'run', side_effect=clean()), self.assertRaisesRegex(ValueError, 'differs'):
            plan.controller(Path('/controller'), {**selected(), 'controller_tree': 'c' * 40})
        dirty = [SimpleNamespace(stdout=' M scripts/linux_release/hosted/installed.py')]
        with patch.object(plan.subprocess, 'run', side_effect=dirty), self.assertRaisesRegex(ValueError, 'outside its immutable'):
            plan.observed_controller(Path('/controller'))

    def test_dispatch_inputs_become_one_selection_bound_to_the_checkout(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / 'selection.json'
            arguments = ['--checkout', '/controller', '--architecture', 'arm64', '--candidate-run-id', '12',
                         '--candidate-artifact-id', '34', '--candidate-controller-commit', 'f' * 40,
                         '--source-commit', '1' * 40, '--source-tree', '2' * 40, '--output', str(output)]
            with patch.object(plan, 'observed_controller', return_value=('a' * 40, 'b' * 40)):
                self.assertEqual(plan.main(arguments), 0)
            self.assertEqual(plan.read(output), selected())
            with patch.object(plan, 'observed_controller', return_value=('a' * 40, 'b' * 40)), \
                    self.assertRaises(SystemExit):
                plan.main(arguments)
            with patch.object(plan, 'observed_controller', return_value=('a' * 40, 'b' * 40)), \
                    self.assertRaises(SystemExit):
                plan.main([*arguments[:-1], str(Path(temporary) / 'other.json'), '--architecture', 'riscv64'])


class HostedRuntimeEntryTests(unittest.TestCase):
    def arguments(self, mode, directory):
        return [mode, '--selection', str(directory / 's.json'), '--checkout', '/controller',
                '--facts', str(directory / 'f.json'), '--output', str(directory / 'out')]

    def test_retention_fails_the_step_unless_the_campaign_or_diagnosis_is_accepted(self):
        from linux_release.hosted import diagnostic
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            with patch.object(runtime, 'selection', return_value=selected()), patch.object(runtime, 'read', return_value={}):
                for accepted, code in ((True, 0), (False, 2)):
                    report = {'complete_matrix_accepted': accepted, 'case_counts': {'passed': 1, 'failed': 0, 'unverified': 1}}
                    with self.subTest(accepted=accepted), patch.object(runtime, 'retain', return_value=report):
                        self.assertEqual(runtime.main(self.arguments('retain', directory)), code)
                for status, code in (('passed', 0), ('unverified', 2), ('failed', 2)):
                    with self.subTest(status=status), patch.object(diagnostic, 'retain', return_value={'status': status}):
                        self.assertEqual(runtime.main(self.arguments('retain-diagnostic', directory)), code)

    def test_desktop_preparation_runs_through_the_lane(self):
        from linux_release.hosted import desktop as hosted_desktop
        with patch.object(hosted_desktop, 'prepare') as prepare:
            self.assertEqual(runtime.main(['desktop', '--checkout', '/controller', '--output', '/facts.json']), 0)
        prepare.assert_called_once_with(Path('/controller'), Path('/facts.json'), None)


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
                (custody / 'contract.json').chmod(0o600)
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

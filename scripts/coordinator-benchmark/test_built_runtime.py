import json
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import built_runtime
import preparation
import run
import prepare as target_preparation
from progress import save
from snapshot import file_hash, identity, source_files
from source_layout import product_entries


class BuiltRuntimeTests(unittest.TestCase):
    def metadata(self, source):
        manifest = {name:file_hash(source/name) for name in source_files(source)}
        contract = {'revision':1,'benchmark_paths':['scripts/coordinator-benchmark/'],
                    'harness_paths':['lycaon/internal/harnessfixture/']}
        product = product_entries(manifest, contract)
        save(source / 'source-manifest.json', manifest)
        save(source / 'source-modes.json', {name:(source/name).stat().st_mode & 0o777 for name in manifest})
        save(source / 'target-contract.json', contract)
        save(source / 'product-manifest.json', product)
        save(source / 'evaluation-source.json', {'benchmark_sha256':identity({k:v for k,v in manifest.items() if k.startswith('scripts/coordinator-benchmark/')}),
             'harness_sha256':identity({}), 'benchmark_source':{}, 'harness_source':{}})
        save(source / 'source.json', {'version':'1.0.0-rc.1', 'revision':'revision', 'dirty':False,
             'source_sha256':identity(product), 'contract_sha256':identity(contract),
             'target':{'kind':'tag','revision':'revision'}})

    def fixture(self, root):
        source = root / 'source'
        runtime = source / built_runtime.RUNTIME
        runtime.mkdir(parents=True)
        (runtime / 'scanner').write_text('seed scanner')
        (source / 'VERSION').write_text('1.0.0-rc.1')
        (source / 'schemas').mkdir()
        (source / 'schemas/oar.json').write_text('selected application schema')
        save(root / 'selection.json', {'application': 'fixture'})
        self.metadata(source)
        for name in built_runtime.EXECUTABLES:
            path = source / name
            path.parent.mkdir(exist_ok=True)
            path.write_text(name)
            path.chmod(0o755)
        return source, runtime

    def test_staging_changes_runtime_then_seal_survives_resume_without_rebuilding(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            source, runtime = self.fixture(root)
            provenance = preparation.source_snapshot(root)

            def build(command, **kwargs):
                self.assertEqual(kwargs['env']['PW_BUILD_DIR'], str(source / '.bin'))
                (runtime / 'scanner').write_text('built scanner')
                return subprocess.CompletedProcess(command, 0)

            with patch.object(preparation.subprocess, 'run', side_effect=build) as build:
                application = preparation.build_application(root, {'PW_BUILD_DIR': '/runner/checkout/build'})
                self.assertEqual(preparation.build_application(root, {}), application)
                self.assertEqual(build.call_count, 1)
            self.assertNotIn('runtime_sha256', provenance)
            self.assertEqual(preparation.source_snapshot(root), provenance)
            self.assertEqual(built_runtime.verify_application(source), application)
            self.assertEqual((runtime / 'schemas/oar.json').read_text(), 'selected application schema')
            self.assertEqual(json.loads((source / 'build-manifest.json').read_text())['runtime']['scanner']['sha256'],
                             file_hash(runtime / 'scanner'))

    def test_interrupted_build_keeps_source_and_retries_without_a_seal(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            source, runtime = self.fixture(root)
            original = (source / 'source.json').read_bytes()

            def failed(command, **kwargs):
                (runtime / 'scanner').write_text('partially staged')
                raise subprocess.CalledProcessError(1, command)

            with patch.object(preparation.subprocess, 'run', side_effect=failed):
                with self.assertRaises(subprocess.CalledProcessError):
                    preparation.build_application(root, {})
            self.assertFalse((source / 'application.json').exists())
            with patch.object(preparation, 'freeze') as freeze:
                preparation.source_snapshot(root)
                freeze.assert_not_called()
            self.assertEqual((source / 'source.json').read_bytes(), original)
            with patch.object(preparation.subprocess, 'run') as build:
                preparation.build_application(root, {})
                build.assert_called_once()

    def test_seal_publication_is_atomic_and_recoverable_before_admission(self):
        for marker in ('build-manifest.json', 'application.json'):
            for published in (False, True):
                with self.subTest(marker=marker, published=published), tempfile.TemporaryDirectory() as temporary:
                    source, _ = self.fixture(pathlib.Path(temporary))
                    replace = pathlib.Path.replace

                    def interrupt(path, destination):
                        if destination.name != marker:
                            return replace(path, destination)
                        if published:
                            replace(path, destination)
                        raise OSError('publication interrupted')

                    with patch.object(pathlib.Path, 'replace', interrupt):
                        with self.assertRaisesRegex(OSError, 'publication interrupted'):
                            built_runtime.seal_application(source)
                    self.assertEqual((source / 'application.json').exists(), marker == 'application.json' and published)
                    recovered = built_runtime.seal_application(source)
                    self.assertEqual(built_runtime.verify_application(source), recovered)

    def test_process_death_during_sealing_leaves_recoverable_metadata(self):
        with tempfile.TemporaryDirectory() as temporary:
            source, _ = self.fixture(pathlib.Path(temporary))
            script = '''import os, pathlib, sys
from built_runtime import seal_application
replace = pathlib.Path.replace
def interrupt(path, destination):
    if destination.name == 'application.json':
        os._exit(17)
    return replace(path, destination)
pathlib.Path.replace = interrupt
seal_application(pathlib.Path(sys.argv[1]))
'''
            result = subprocess.run([sys.executable, '-c', script, str(source)],
                                    cwd=pathlib.Path(built_runtime.__file__).parent)
            self.assertEqual(result.returncode, 17)
            self.assertFalse((source/'application.json').exists())
            recovered = built_runtime.seal_application(source)
            self.assertEqual(built_runtime.verify_application(source), recovered)

    def test_sealed_artifact_mutations_cannot_be_rebuilt_or_resealed(self):
        for change in ('scanner', 'engine', 'driver', 'logs', 'permissions', 'directory', 'link', 'added', 'deleted'):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                source, runtime = self.fixture(root)
                (runtime / 'other').write_text('other')
                (runtime / 'bin').mkdir()
                (runtime / 'alias').symlink_to('scanner')
                built_runtime.seal_application(source)
                if change in ('scanner', 'engine', 'driver', 'logs'):
                    path = {'scanner': runtime / 'scanner', 'engine': source / '.bin/lycaon-dev',
                            'driver': source / '.bin/lycaon-debug', 'logs': source / '.bin/pw-logs'}[change]
                    path.write_text('changed')
                elif change == 'permissions':
                    (source / '.bin/lycaon-dev').chmod(0o644)
                elif change == 'directory':
                    (runtime / 'bin').chmod(0o700)
                elif change == 'link':
                    (runtime / 'alias').unlink()
                    (runtime / 'alias').symlink_to('other')
                elif change == 'added':
                    (runtime / 'new').write_text('new')
                else:
                    (runtime / 'other').unlink()
                with patch.object(preparation.subprocess, 'run') as build:
                    with self.assertRaisesRegex(ValueError, 'sealed application artifacts changed'):
                        preparation.build_application(root, {})
                    build.assert_not_called()
                with self.assertRaisesRegex(ValueError, 'sealed application artifacts changed'):
                    built_runtime.seal_application(source)

    def test_missing_seal_after_admission_cannot_be_reconstructed(self):
        for marker in ('plan.json', 'captures', 'provider-preflight'):
            with self.subTest(marker=marker), tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                source, _ = self.fixture(root)
                built_runtime.seal_application(source)
                (source / 'application.json').unlink()
                (root / marker).mkdir() if '.' not in marker else (root / marker).write_text('{}')
                with self.assertRaisesRegex(ValueError, 'after execution admission'):
                    built_runtime.seal_application(source)
                with patch.object(preparation.subprocess, 'run') as build:
                    with self.assertRaisesRegex(ValueError, 'after execution admission'):
                        preparation.build_application(root, {})
                    build.assert_not_called()

    def test_runtime_requires_contained_links_and_regular_artifacts(self):
        for kind in ('external', 'dangling', 'fifo'):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                source, runtime = self.fixture(root)
                path = runtime / 'invalid'
                if kind == 'fifo':
                    os.mkfifo(path)
                else:
                    path.symlink_to(source / 'VERSION' if kind == 'external' else runtime / 'missing')
                with self.assertRaises(ValueError):
                    built_runtime.seal_application(source)

    def test_admitted_application_identity_must_match_the_verified_build(self):
        with tempfile.TemporaryDirectory() as temporary:
            source, _ = self.fixture(pathlib.Path(temporary))
            application = built_runtime.seal_application(source)
            models = [{'configuration': {'application': application}}]
            self.assertEqual(built_runtime.verify_application(source, models), application)
            with self.assertRaisesRegex(ValueError, 'admitted plan'):
                built_runtime.verify_application(source, [{'configuration': {'application': {}}}])

    def test_preflight_mutation_stops_before_configuration_and_paid_admission(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            source, runtime = self.fixture(root)
            implementation = source / 'scripts/coordinator-benchmark'
            implementation.mkdir(parents=True)
            save(implementation / 'benchmark.json', {'revision':1, 'application_contract':1, 'suite':'suite.json', 'operations':[{'id':'current-verification'}]})
            save(source / 'suite.json', {})
            args = type('Arguments', (), {'out': root})()
            self.metadata(source)

            def preflight(*args):
                (runtime / 'scanner').write_text('mutated by preflight')
                return {}

            with patch.object(run, 'inputs', return_value={'mode':'exploration','tier':'all','cases':'current-verification'}), \
                    patch.object(target_preparation, 'identity', return_value=run.IMPLEMENTATION), \
                    patch.object(preparation.subprocess, 'run'), \
                    patch.object(target_preparation, 'validate_runtime', side_effect=preflight), \
                    patch.object(run, 'model_configurations') as configure, patch.object(run, 'ProviderAdmission') as admit:
                with self.assertRaisesRegex(ValueError, 'sealed application artifacts changed'):
                    run.prepare(args, root / 'credentials')
                configure.assert_not_called()
                admit.assert_not_called()
            self.assertFalse((root / 'plan.json').exists())

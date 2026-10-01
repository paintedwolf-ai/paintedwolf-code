"""Exercise retained preparation and repair without providers or application processes."""
import argparse
import json
import pathlib
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import prepare
import preparation
import target_selection
from built_runtime import EXECUTABLES, RUNTIME, seal_application, verify_application
from grading_identity import implementation_files
from progress import save
from snapshot import file_hash, identity
from source_layout import freeze_sources
from test_snapshot import repository, commit
from operation_selection import selected_operations


def binaries(source):
    runtime=source/RUNTIME;runtime.mkdir(parents=True,exist_ok=True)
    (runtime/'tool').write_text('pinned application tool')
    for name in EXECUTABLES:
        path=source/name;path.parent.mkdir(exist_ok=True);path.write_text(name);path.chmod(0o755)


class PreparedTargetTests(unittest.TestCase):
    def test_explicit_benchmark_revision_continues_in_its_frozen_preparer_before_building(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            repo = root/'repo'
            target = repository(repo)
            manifest_path = repo/'scripts/coordinator-benchmark/benchmark.json'
            manifest = json.loads(manifest_path.read_text())
            manifest['operations'] = [{'id':'one'}]
            save(manifest_path, manifest)
            (repo/'scripts/coordinator-benchmark/prepare.py').write_text('selected preparer')
            benchmark = commit(repo, 'benchmark-revision')
            destination = root/'prepared'
            destination.mkdir()
            selection = {'application':target, 'benchmark':benchmark, 'cases':['one']}
            with patch.object(preparation, 'freeze', side_effect=lambda path, selected: freeze_sources(repo, path, selected)), \
                 patch.object(prepare.os, 'execv', side_effect=RuntimeError('frozen preparer')) as execute, \
                 patch.object(prepare, 'build_application') as build:
                with self.assertRaisesRegex(RuntimeError, 'frozen preparer'):
                    prepare.prepare_target(destination, selection, '0'*64, ['one'])
                build.assert_not_called()
            command = execute.call_args.args[1]
            self.assertEqual(command[1:], [str(destination/'source/scripts/coordinator-benchmark/python_runtime.py'),
                                           str(destination/'source/scripts/coordinator-benchmark/prepare.py'),
                                           '--resume', '--out', str(destination)])
            self.assertEqual(json.loads((destination/'selection.json').read_text()), selection)

    def test_application_and_benchmark_refs_are_selected_independently(self):
        with tempfile.TemporaryDirectory() as temporary:
            repo = pathlib.Path(temporary)/'repo'
            target = repository(repo)
            (repo/'scripts/coordinator-benchmark/run.py').write_text('updated evaluator')
            benchmark = commit(repo, 'benchmark-revision')
            parser = argparse.ArgumentParser()
            target_selection.add_arguments(parser)
            parser.add_argument('--benchmark-ref')
            args = parser.parse_args(['--application-ref','v0.9.0','--benchmark-ref','benchmark-revision'])
            with patch.object(target_selection, 'ROOT', repo), patch.object(target_selection, 'working_tree') as capture:
                selection = target_selection.select(args)
                capture.assert_not_called()
            self.assertEqual(selection['application'], target)
            self.assertEqual(selection['benchmark'], benchmark)

    def test_execution_cannot_relabel_the_sealed_evaluator(self):
        with tempfile.TemporaryDirectory() as temporary:
            _, root, marker = self.fixture(pathlib.Path(temporary))
            plan = {'mode':'release', 'lineage':None,
                    'benchmark':{'application_version':marker['application']['version'],
                                 'evaluation':marker['evaluation']}}
            self.assertEqual(verify_application(root/'source', plan=plan), marker['application'])
            plan['benchmark']['evaluation'] = {**marker['evaluation'], 'driver_sha256':'0'*64}
            with self.assertRaisesRegex(ValueError, 'sealed artifacts'):
                verify_application(root/'source', plan=plan)

    def fixture(self, root):
        repo=root/'repo';revision=repository(repo)
        destination=root/'prepared';destination.mkdir()
        selection={'application':revision,'benchmark':revision}
        freeze_sources(repo,destination/'source',selection)
        save(destination/'selection.json',selection)
        binaries(destination/'source')
        application=seal_application(destination/'source')
        receipt=destination/'preflight-fixture/application/validation.json'
        receipt.parent.mkdir(parents=True)
        save(receipt,{'cases':['one','two']})
        marker={'version':1,'application':application,
                'evaluation':json.loads((destination/'source/evaluation.json').read_text()),
                'implementation_sha256':identity(implementation_files(destination/'source/scripts/coordinator-benchmark')),
                'preflight':{'receipt':str(receipt.relative_to(destination)),'sha256':file_hash(receipt)},
                'cases':['one','two'],'lineage':None}
        save(destination/'prepared.json',marker)
        return repo,destination,marker

    def test_reuse_verifies_and_copies_artifacts_without_building_or_preflight(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=pathlib.Path(temporary);_,original,marker=self.fixture(root)
            destination=root/'run';destination.mkdir()
            with patch.object(prepare,'build_application') as build,patch.object(prepare,'validate_runtime') as preflight:
                actual=prepare.prepare_target(destination,{'prepared':str(original)},marker['implementation_sha256'],['one'])
                self.assertEqual(actual,marker)
                build.assert_not_called();preflight.assert_not_called()
            self.assertEqual(prepare.verify_prepared(destination),marker)
            self.assertFalse((original/'source/.bin/lycaon-dev').samefile(destination/'source/.bin/lycaon-dev'))

    def test_partial_controls_cannot_authorize_a_larger_bank(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=pathlib.Path(temporary);_,original,marker=self.fixture(root)
            destination=root/'run';destination.mkdir()
            with self.assertRaisesRegex(ValueError,'do not cover'):
                prepare.prepare_target(destination,{'prepared':str(original)},marker['implementation_sha256'],['three'])
            self.assertFalse((destination/'source').exists())
            save(original/'prepared.json',{**marker,'cases':['one','two','three']})
            with self.assertRaisesRegex(ValueError,'coverage'):prepare.verify_prepared(original)

    def test_interrupted_import_retries_only_its_owned_artifacts(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=pathlib.Path(temporary);_,original,marker=self.fixture(root)
            destination=root/'run';destination.mkdir()
            copy=shutil.copytree
            def interrupted(source,target,*args,**kwargs):
                if pathlib.Path(source)==original/'source':
                    pathlib.Path(target).mkdir();(pathlib.Path(target)/'partial').write_text('partial')
                    raise OSError('interrupted import')
                return copy(source,target,*args,**kwargs)
            with patch.object(prepare.shutil,'copytree',side_effect=interrupted),self.assertRaisesRegex(OSError,'interrupted'):
                prepare.import_prepared(original,destination)
            self.assertEqual(prepare.import_prepared(original,destination),marker)
            self.assertFalse((destination/'source/partial').exists())
            self.assertEqual(prepare.verify_prepared(original),marker)

    def test_external_repair_reuses_engine_and_runtime_and_builds_only_driver(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=pathlib.Path(temporary);repo,original,marker=self.fixture(root)
            target=marker['application']['target']
            (repo/'lycaon/internal/eval/driver.go').write_text('repaired collector')
            benchmark=commit(repo,'benchmark-repair')
            destination=root/'repair';destination.mkdir()
            selection={'application':target,'benchmark':benchmark,'retained_source':str(original/'source'),
                       'harness':target,'replace_harness':False}
            freeze_sources(repo,destination/'source',selection)
            save(destination/'selection.json',selection)
            def build(command,**kwargs):
                self.assertEqual(command,['./task','eval:tool-usage','BUILD_ONLY=true'])
                self.assertEqual(kwargs['env']['GOWORK'], 'off')
                self.assertEqual(kwargs['env']['GOFLAGS'], '-buildvcs=false')
                self.assertEqual(kwargs['env']['PATH'], '/toolchain/bin')
                path=destination/'source/.bin/lycaon-debug';path.write_text('repaired binary');path.chmod(0o755)
                return subprocess.CompletedProcess(command,0)
            environment = {'GOWORK':'/outside/go.work', 'GOFLAGS':'-tags=unexpected', 'PATH':'/toolchain/bin'}
            with patch.object(preparation.subprocess,'run',side_effect=build):
                application=preparation.build_application(destination,environment)
            self.assertEqual(environment['GOFLAGS'], '-tags=unexpected')
            self.assertEqual(application,marker['application'])
            self.assertEqual((destination/'source/.bin/lycaon-dev').read_bytes(),(original/'source/.bin/lycaon-dev').read_bytes())
            self.assertNotEqual(json.loads((destination/'source/evaluation.json').read_text())['build_sha256'],marker['evaluation']['build_sha256'])
            self.assertEqual(prepare.verify_prepared(original),marker)

    def test_repair_selection_records_lineage_and_preserves_the_application(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=pathlib.Path(temporary);_,original,marker=self.fixture(root)
            parser=argparse.ArgumentParser();target_selection.add_arguments(parser)
            args=parser.parse_args(['--repair-from',str(original),'--reason','Fix collection'])
            with patch.object(target_selection,'working_tree',return_value=marker['application']['target']):
                selected=target_selection.select(args)
            self.assertEqual(selected['application'],marker['application']['target'])
            self.assertEqual(selected['lineage']['reason'],'Fix collection')
            self.assertEqual(selected['lineage']['previous_evaluation_sha256'],file_hash(original/'source/evaluation.json'))
            args.harness_worktree = True
            benchmark = {**marker['application']['target'], 'revision':'b'*40}
            with patch.object(target_selection, 'working_tree', return_value=benchmark):
                selected = target_selection.select(args)
            self.assertEqual(selected['harness'], benchmark)
            self.assertTrue(selected['replace_harness'])
            self.assertEqual(selected['application'], marker['application']['target'])
            args.reason=None
            with self.assertRaisesRegex(ValueError,'requires'):target_selection.select(args)

    def test_changed_engine_and_receipt_are_rejected_before_import(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=pathlib.Path(temporary);_,original,marker=self.fixture(root)
            engine=original/'source/.bin/lycaon-dev';body=engine.read_bytes();engine.write_text('changed')
            with self.assertRaisesRegex(ValueError,'artifacts changed'):prepare.verify_prepared(original)
            engine.write_bytes(body)
            (original/marker['preflight']['receipt']).write_text('{}')
            with self.assertRaisesRegex(ValueError,'preflight changed'):prepare.verify_prepared(original)


class OperationSelectionTests(unittest.TestCase):
    def test_selection_obeys_the_declared_mode_and_tier(self):
        bank={'operations':[{'id':'a','role':'scored','tier':'gate'},
                            {'id':'b','role':'scored','tier':'orchestration'},
                            {'id':'c','role':'calibration'}]}
        self.assertEqual(selected_operations(bank),['a','b'])
        self.assertEqual(selected_operations(bank,'calibration'),['c'])
        self.assertEqual(selected_operations(bank,tier='gate'),['a'])
        for mode,tier,cases in [('release','gate','b'),('exploration','all','c'),('calibration','all','a'),
                                ('exploration','all',''),('exploration','all','a,')]:
            with self.subTest(mode=mode,tier=tier,cases=cases),self.assertRaises(ValueError):
                selected_operations(bank,mode,tier,cases)

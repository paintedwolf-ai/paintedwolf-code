"""Exercise application pinning and evaluator repairs against real isolated Git histories."""
import json
import pathlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import preparation
import snapshot
import source_revision
from source_layout import freeze_sources, owns
from progress import save


def repository(root):
    root.mkdir()
    subprocess.run(['git', 'init', '-q', str(root)], check=True)
    contract = json.loads((snapshot.ROOT/source_revision.CONTRACT).read_text())
    files = {
        'VERSION': '0.9.0\n', 'RELEASE_BUILD': '1\n',
        'schemas/oar/oar.schema.json': '{"version":"original"}',
        'lycaon/internal/product/main.go': 'package product\nconst Behavior = "original"\n',
        'lycaon/internal/harnessfixture/helper.go': 'package harnessfixture\n',
        source_revision.CONTRACT: json.dumps(contract),
        'scripts/coordinator-benchmark/benchmark.json': json.dumps({'revision': 1, 'application_contract': contract['revision']}),
        'scripts/coordinator-benchmark/run.py': 'original runner',
        'lycaon/internal/eval/driver.go': 'original driver',
        'lycaon/test/fixtures/eval/task.json': '{}',
        'task': '#!/bin/sh\nexit 0\n',
    }
    for name, body in files.items():
        path=root/name; path.parent.mkdir(parents=True, exist_ok=True); path.write_text(body)
    (root/'task').chmod(0o755)
    revision = commit(root, 'v0.9.0')
    return revision


def commit(root, tag):
    subprocess.run(['git', 'add', '--all'], cwd=root, check=True)
    subprocess.run(['git', '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid',
                    'commit', '-qm', 'Fixture revision'], cwd=root, check=True)
    subprocess.run(['git', 'tag', tag], cwd=root, check=True)
    return source_revision.resolve(root, tag)


class SnapshotTests(unittest.TestCase):
    def test_file_boundaries_do_not_match_nested_product_directories(self):
        paths = ['lycaon/internal/api/harness_*.go', 'lycaon/internal/harnessfixture/']
        for name, expected in [
            ('lycaon/internal/api/harness_contract.go', True),
            ('lycaon/internal/api/harness_protocol/product.go', False),
            ('lycaon/internal/api/server.go', False),
            ('other/lycaon/internal/api/harness_contract.go', False),
            ('lycaon/internal/harnessfixture/nested/helper.go', True),
            ('lycaon/internal/harnessfixture_product/handler.go', False),
        ]:
            with self.subTest(path=name):
                self.assertEqual(owns(name, paths), expected)

    def test_new_evaluator_cannot_replace_target_product_or_version(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=pathlib.Path(temporary); repo=root/'repo'
            target=repository(repo)
            (repo/'VERSION').write_text('0.10.0\n')
            (repo/'schemas/oar/oar.schema.json').write_text('{"version":"new"}')
            (repo/'lycaon/internal/product/main.go').write_text('new product')
            (repo/'scripts/coordinator-benchmark/run.py').write_text('corrected runner')
            (repo/'lycaon/internal/eval/driver.go').write_text('corrected driver')
            benchmark=commit(repo, 'benchmark-2')
            destination=root/'source'
            app=freeze_sources(repo,destination,{'application':target,'benchmark':benchmark})
            self.assertEqual(app['version'],'0.9.0')
            self.assertEqual(app['revision'],target['revision'])
            self.assertIn('original', (destination/'lycaon/internal/product/main.go').read_text())
            self.assertEqual(json.loads((destination/'schemas/oar/oar.schema.json').read_text()), {'version':'original'})
            self.assertEqual((destination/'lycaon/internal/eval/driver.go').read_text(),'corrected driver')
            self.assertEqual(snapshot.read_source(destination),app)
            self.assertFalse(app['dirty'])

    def test_harness_repair_preserves_product_identity_and_records_different_evaluation(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=pathlib.Path(temporary); repo=root/'repo'; target=repository(repo)
            original=root/'original'
            before=freeze_sources(repo,original,{'application':target,'benchmark':target})
            (repo/'lycaon/internal/harnessfixture/helper.go').write_text('corrected harness')
            (repo/'lycaon/internal/product/main.go').write_text('unrelated new application behavior')
            repaired=commit(repo,'harness-2')
            after=freeze_sources(repo,root/'repaired',{'application':target,'benchmark':repaired,'harness':repaired})
            self.assertEqual(before,after)
            self.assertNotEqual(json.loads((original/'evaluation-source.json').read_text())['harness_sha256'],
                                json.loads((root/'repaired/evaluation-source.json').read_text())['harness_sha256'])
            retained=freeze_sources(repo,root/'retained',{'application':target,'benchmark':repaired,
                'retained_source':str(original),'harness':target})
            self.assertEqual(retained,before)
            self.assertEqual((root/'retained/lycaon/internal/harnessfixture/helper.go').read_text(),'package harnessfixture\n')

    def test_evaluator_cannot_widen_the_targets_evaluation_boundary(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=pathlib.Path(temporary);repo=root/'repo';target=repository(repo)
            contract=json.loads((repo/source_revision.CONTRACT).read_text())
            contract['harness_paths'].append('lycaon/internal/product/')
            save(repo/source_revision.CONTRACT,contract)
            benchmark=commit(repo,'unsafe-boundary')
            with self.assertRaisesRegex(ValueError,'contract'):
                freeze_sources(repo,root/'source',{'application':target,'benchmark':benchmark})

    def test_benchmark_must_declare_the_application_contract(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=pathlib.Path(temporary);repo=root/'repo';target=repository(repo)
            path=repo/'scripts/coordinator-benchmark/benchmark.json'
            manifest=json.loads(path.read_text())
            manifest['application_contract'] += 1
            save(path,manifest)
            benchmark=commit(repo,'mismatched-contract')
            with self.assertRaisesRegex(ValueError,'application evaluation contract'):
                freeze_sources(repo,root/'source',{'application':target,'benchmark':benchmark})

    def test_tag_resolution_is_immutable_and_branches_are_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            repo=pathlib.Path(temporary)/'repo';target=repository(repo)
            self.assertEqual(source_revision.resolve(repo,target['revision'])['revision'],target['revision'])
            for ref in ('HEAD','main','--help','v0.9.0~1',target['revision'][:12]):
                with self.subTest(ref=ref),self.assertRaises(ValueError): source_revision.resolve(repo,ref)
            subprocess.run(['git','tag','-d','v0.9.0'],cwd=repo,check=True,stdout=subprocess.DEVNULL)
            app=freeze_sources(repo,pathlib.Path(temporary)/'source',{'application':target,'benchmark':target})
            self.assertEqual(app['revision'],target['revision'])

    def test_source_additions_permissions_and_product_mutations_are_detected(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=pathlib.Path(temporary);repo=root/'repo';target=repository(repo)
            for mutation in ('added','edited','permissions'):
                destination=root/mutation
                freeze_sources(repo,destination,{'application':target,'benchmark':target})
                if mutation=='added': (destination/'lycaon/internal/product/injected.go').write_text('injected')
                elif mutation=='edited': (destination/'lycaon/internal/product/main.go').write_text('changed')
                else: (destination/'task').chmod(0o644)
                with self.subTest(mutation=mutation),self.assertRaisesRegex(ValueError,'source'):
                    snapshot.read_source(destination)

    def test_interrupted_marker_publication_keeps_the_selected_target_on_retry(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=pathlib.Path(temporary);repo=root/'repo';target=repository(repo);run=root/'run';run.mkdir()
            selection={'application':target,'benchmark':target}
            replace=pathlib.Path.replace
            def interrupt(path,destination):
                if destination.name=='source.json': raise OSError('injected publication interruption')
                return replace(path,destination)
            with patch.object(snapshot,'ROOT',repo):
                with patch.object(pathlib.Path,'replace',interrupt),self.assertRaisesRegex(OSError,'injected'):
                    preparation.source_snapshot(run,selection)
                recovered=preparation.source_snapshot(run)
                self.assertEqual(recovered['revision'],target['revision'])
                self.assertEqual(preparation.source_snapshot(run),recovered)
                with self.assertRaisesRegex(ValueError,'selection changed'):
                    preparation.source_snapshot(run,{**selection,'extra':'changed'})

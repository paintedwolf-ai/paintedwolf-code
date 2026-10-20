"""Native candidate bytes grant fixture identity without installed or publication authority."""
import hashlib
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import Mock, patch

from linux_release import installed_candidate_inputs as owner


class CandidateInstalledInputsTests(unittest.TestCase):
    def fixture(self, root, partial=False):
        payload=root/'artifact'
        payload.mkdir()
        source={'source_commit':'a'*40,'source_tree':'b'*40,'version':'1.2.3','release_build':14,'channel':'stable'}
        run={'id':11,'head_sha':'c'*40,'repository':{'id':7,'full_name':'owner/repo'},'head_repository':{'id':7},
             'path':'.github/workflows/linux-candidates.yml','head_branch':'linux/candidate-ci-isolation',
             'event':'push','status':'completed','conclusion':'failure' if partial else 'success','run_attempt':1}
        selection={'artifact_id':12,'artifact_bytes':100,'artifact_sha256':'d'*64}
        rows=[]
        for family,extension in owner.EXTENSIONS.items():
            if partial and family=='pacman':
                continue
            name='painted-wolf-code_v1.2.3_linux-aarch64.'+extension
            raw=('actual '+family+' selected bytes').encode()
            (payload/name).write_bytes(raw)
            row={'file':name,'sha256':hashlib.sha256(raw).hexdigest(),'bytes':len(raw)}
            if partial:
                row['source_path']='lycaon-den/src-tauri/target/aarch64-unknown-linux-gnu/release/bundle/'+family+'/'+name
            else:
                row.update(family=family,architecture='aarch64')
            rows.append(row)
        if partial:
            document={'format_version':1,'scope':'partial-build-diagnostics','source_commit':'a'*40,'source_tree':'b'*40,
                      'controller_commit':'c'*40,'complete_candidate':False,'installed_qualified':False,
                      'publication_authority':False,'packages':rows}
            (payload/'diagnostics.json').write_text(json.dumps(document))
        else:
            document={'format_version':1,'source_commit':'a'*40,'version':'1.2.3','platform':'linux-aarch64',
                      'architecture':'aarch64','packages':rows,'qualification_status':'unverified','published':False}
            (payload/'candidate-packages.json').write_text(json.dumps(document))
            for name,value in [('source-commit.txt','a'*40),('source-tree.txt','b'*40),('controller-commit.txt','c'*40)]:
                (payload/name).write_text(value+'\n')
        return payload,source,run,selection

    def prepare(self, root, fixture, partial=False):
        payload,source,run,selection=fixture
        with patch.object(owner,'artifact_for_run',return_value=selection),patch.object(owner,'facts',return_value=source), \
                patch.object(owner,'download',side_effect=lambda _,destination:shutil.copytree(payload,destination)):
            return owner.prepare(lambda _:run,'owner/repo',7,11,12,'c'*40,root/'source','a'*40,'b'*40,
                                 'arm64',root/'prepared',partial=partial)

    def test_complete_candidate_retains_exact_three_families_without_qualification_claim(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            fixture=self.fixture(root)
            report=self.prepare(root,fixture)
            final=json.loads((root/'prepared/final-artifacts.json').read_text())
            self.assertEqual({row['installation'] for row in final['artifacts']},{'deb','rpm','pacman'})
            self.assertFalse(report['qualification_authority'])
            self.assertFalse(report['publication_authority'])
            self.assertFalse(report['partial'])
            for row in final['artifacts']:
                path=root/'prepared/packages'/row['id']
                self.assertEqual(hashlib.sha256(path.read_bytes()).hexdigest(),row['identity']['sha256'])
                self.assertEqual(path.stat().st_mode & 0o777,0o600)

    def test_partial_diagnostics_can_feed_functional_cases_but_remain_explicitly_partial(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            report=self.prepare(root,self.fixture(root,True),True)
            final=json.loads((root/'prepared/final-artifacts.json').read_text())
            self.assertEqual({row['installation'] for row in final['artifacts']},{'deb','rpm'})
            self.assertTrue(report['partial'])
            self.assertIs(report['qualification_authority'],False)

    def test_wrong_source_cpu_and_tampered_bytes_refuse_without_retained_output(self):
        for mutation in ('source','cpu','bytes'):
            with self.subTest(mutation=mutation),tempfile.TemporaryDirectory() as temporary:
                root=Path(temporary)
                fixture=self.fixture(root)
                payload=fixture[0]
                if mutation=='source':
                    (payload/'source-commit.txt').write_text('e'*40)
                elif mutation=='cpu':
                    path=payload/'candidate-packages.json'
                    document=json.loads(path.read_text());document['architecture']='x86_64';path.write_text(json.dumps(document))
                else:
                    next(payload.glob('*.deb')).write_bytes(b'changed')
                with self.assertRaises(ValueError):
                    self.prepare(root,fixture)
                self.assertFalse((root/'prepared').exists())

    def test_unrelated_failed_or_fork_run_cannot_query_candidate_artifacts(self):
        with tempfile.TemporaryDirectory() as temporary:
            _,_,run,_=self.fixture(Path(temporary))
            for key,value in [('head_sha','e'*40),('head_branch','feature'),('conclusion','failure'),
                              ('head_repository',{'id':99}),('path','.github/workflows/release.yml')]:
                with self.subTest(key=key),patch.object(owner,'artifact_for_run') as artifact:
                    with self.assertRaises(ValueError):
                        owner.select(lambda _:{**run,key:value},'owner/repo',7,11,12,'c'*40,'arm64',False)
                    artifact.assert_not_called()

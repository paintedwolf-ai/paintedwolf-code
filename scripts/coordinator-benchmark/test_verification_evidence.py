import copy
import pathlib
from test_episode_support import EvidenceCase
from verification_evidence import verification_checks, suite_command
from snapshot import ROOT
from report import measurement_inputs
from measurement_cache import measure


class VerificationEvidenceTests(EvidenceCase):
    def setUp(self):
        super().setUp()
        self.capture=self.root
        self.case={'session_id':'root'}
        self.record={'type':'verify','slot':'tests','run_id':'root','verdict':'passed','artifacts':{
            'session_id':'root','check_id':'call','command':'python3 -B -m unittest discover',
            'is_check':True,'exit_code':0,'producer':'verify','cwd':'.','source_revision':'revision','source_root_digest':'root'}}
        self.facts['snapshots']['revision']={'roots_key':'root','quality':'exact','roots':['/original'],'matches_delivered':True}
        self.facts['delivered_paths']=['app.py','test_app.py']

    def passes(self,record=None):
        self.facts['verification']=[record or self.record]
        self.save()
        return verification_checks(self.root,self.case,ROOT)[0]['passed']

    def test_source_equality_is_required_for_every_accepted_command(self):
        for command in ['python -m unittest','python3 -B -m unittest discover -s /original','python -m unittest test_app.py']:
            self.record['artifacts']['command']=command
            self.facts['snapshots']['revision']['matches_delivered']=True
            self.assertTrue(self.passes())
            self.facts['snapshots']['revision']['matches_delivered']=False
            self.assertFalse(self.passes())

    def test_worker_proof_requires_membership_in_the_captured_session_tree(self):
        self.record['artifacts']['session_id']='child'
        self.assertFalse(self.passes())
        self.facts['sessions'].append({'id':'child'})
        self.assertTrue(self.passes())

    def test_unrelated_failed_partial_and_wrong_command_evidence_cannot_pass(self):
        for field,value in [('session_id','stranger'),('is_check',False),('exit_code',1),
                ('producer','read'),('check_id',''),('source_revision','other'),('source_root_digest','other'),
                ('command','true'),('command','python3 -B -m unittest test_filter'),('cwd','subdir')]:
            with self.subTest(field=field,value=value):
                record=copy.deepcopy(self.record)
                record['artifacts'][field]=value
                self.assertFalse(self.passes(record))
        for field,value in [('run_id','other'),('verdict','failed'),('slot','other')]:
            record=copy.deepcopy(self.record);record[field]=value
            self.assertFalse(self.passes(record))

    def test_command_grammar_accepts_equivalent_flags_without_shell_guessing(self):
        for command in ['python3 -B -m unittest discover','python3 -m unittest discover -v',
                        'python3 -B -m unittest "discover" --quiet','python -m unittest discover -v']:
            self.assertTrue(suite_command(command, ['test_filter.py']))
        for command in ['true','echo "python3 -B -m unittest discover"','python3 -B -m unittest discover || true',
                        'python3 -B -m unittest discover -s empty','python3 -m unittest test_other','"',
                        'python -m unittest test_filter.OneTest','python -m unittest discover || true']:
            self.assertFalse(suite_command(command, ['test_filter.py']))

    def test_explicit_modules_must_cover_every_delivered_test_file(self):
        files = ['app.py', 'test_first.py', 'test_second.py']
        for arguments in ['test_first test_second', 'test_second.py test_first.py',
                          '-v test_first.py test_second']:
            self.assertTrue(suite_command('python3 -m unittest ' + arguments, files))
        for arguments in ['test_first', 'test_second.py', 'test_first.SomeTests test_second',
                          'test_first test_second -k one', 'test_first test_second || true']:
            self.assertFalse(suite_command('python3 -m unittest ' + arguments, files))
        self.assertFalse(suite_command('python3 -m unittest test_first test_second',
                                      [*files, 'test_new.py']))

    def test_discovery_options_must_cover_the_root_and_every_supplied_test(self):
        files = ['app.py','test_first.py','test_second.py']
        for arguments in ['-s . -p "test_*.py" -v','--start-directory=./ --pattern=test*.py --top-level-directory=.',
                          '. "test_*.py" .','-s /original -t /original -p "test_*.py"']:
            with self.subTest(arguments=arguments):
                self.assertTrue(suite_command('python3 -B -m unittest discover '+arguments,files,'/original'))
        for arguments in ['-s empty','-s . -t ..','-s /other','-p "test_first.py"',
                          '-s . -k First','--pattern','-p test_*.py || true','--start-directory=. --unknown']:
            with self.subTest(arguments=arguments):
                self.assertFalse(suite_command('python3 -m unittest discover '+arguments,files,'/original'))
        self.assertFalse(suite_command('python3 -m unittest discover -p "test_*.py"',[*files,'testExtra.py']))


    def test_missing_proof_fields_do_not_turn_an_unrelated_check_into_grader_failure(self):
        for key in list(self.record['artifacts']):
            with self.subTest(field=key):
                record=copy.deepcopy(self.record)
                del record['artifacts'][key]
                self.assertFalse(self.passes(record))

    def test_typed_evidence_changes_invalidate_a_cached_measurement(self):
        for name in ['report.json','configuration.json','suite.json','llm-requests.jsonl']:
            (self.root/name).write_text('{}')
        calls=[]
        def evaluate():
            calls.append(True)
            return {'outcome':'passed'}
        self.passes()
        folder=self.root/'episode-evidence';folder.mkdir()
        import shutil
        target=folder/'root.json'
        shutil.copyfile(self.root/'facts.json',target)
        measure(self.root,'grader',measurement_inputs(self.root),evaluate)
        measure(self.root,'grader',measurement_inputs(self.root),evaluate)
        self.assertEqual(len(calls),1)
        self.facts['snapshots']['revision']['matches_delivered']=False
        self.save();shutil.copyfile(self.root/'facts.json',target)
        measure(self.root,'grader',measurement_inputs(self.root),evaluate)
        self.assertEqual(len(calls),2)

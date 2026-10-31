import copy
import shutil
import unittest
from grading_identity import measure
from report import measurement_inputs
from snapshot import ROOT
from test_episode_evidence import EvidenceCase
from test_orchestration_evidence import LedgerCase
from verification_evidence import capability_checks, suite_command, verification_checks


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
        target=folder/'root.json'
        shutil.copyfile(self.root/'facts.json',target)
        measure(self.root,'grader',measurement_inputs(self.root),evaluate)
        measure(self.root,'grader',measurement_inputs(self.root),evaluate)
        self.assertEqual(len(calls),1)
        self.facts['snapshots']['revision']['matches_delivered']=False
        self.save();shutil.copyfile(self.root/'facts.json',target)
        measure(self.root,'grader',measurement_inputs(self.root),evaluate)
        self.assertEqual(len(calls),2)


class CapabilityTests(LedgerCase):
    def setUp(self):
        super().setUp()
        self.case = {'session_id': 'root', 'project_dir': str(self.root / 'project')}
        (self.root / 'project').mkdir()

    def card(self, id_, subject, status, call, created):
        self.session['checkpoints'].append({'id':id_,'kind':'tool_approval','status':status,'issued_at':created,
            'tool_approval':{'tool_call_id':call,'plan':{'subject':{'kind':subject}}}})
        self.save()

    def command(self,ord_,call,capabilities,status='completed',writes=(),committed='2026-09-09T10:00:10Z',program='selfcheck.py',tool='command',python='python3'):
        self.result(ord_,tool,call,{'command':python+' -B '+program,'capability_request':{c:{} for c in capabilities}},
                    outcome='completed' if status=='completed' else 'rejected')
        self.session['invocations'].append({'tool_call_id':call,'tool':tool,'status':status,'invoked':status=='completed',
            'started_at':committed,'settled_at':committed,'source_verdict':'passed' if status=='completed' else 'failed'})
        for path in writes:
            self.facts['effects'].append({'session_id':'root','tool_call_id':call,'tool_name':tool,'committed_ts':committed,'path':path,'op':'write'})
        self.save()

    def receipt(self,call): return next(r for r in self.session['invocations'] if r['tool_call_id']==call)

    def passing(self, expected):
        self.save()
        return {c['id']: c['passed'] for c in capability_checks(self.root, self.case, expected)}

    def test_supplied_program_must_execute_successfully_with_declared_capabilities(self):
        expected = {'executed_with': {'capabilities': ['local_listen', 'loopback_connect'], 'artifact': 'receipt.json', 'program': 'selfcheck.py'}, 'cards_at_most_once': ['local_listen', 'loopback_connect']}
        self.command(1, 'call-1', ['local_listen', 'loopback_connect'], writes=['receipt.json'])
        self.card('a', 'local_listen', 'approved', 'call-1', '2026-09-09T10:00:00Z')
        self.card('b', 'loopback_connect', 'approved', 'call-1', '2026-09-09T10:00:01Z')
        self.assertTrue(all(self.passing(expected).values()), self.passing(expected))
        for receipt in self.session['invocations']: receipt['source_verdict']='failed'
        self.assertFalse(self.passing(expected)['supplied-program-executed-with-capabilities'])
        self.facts['effects'].clear()
        for receipt in self.session['invocations']: receipt.update(status='rejected',invoked=False)
        self.command(2, 'call-2', ['local_listen', 'loopback_connect'], status='rejected', writes=['receipt.json'])
        self.assertFalse(self.passing(expected)['supplied-program-executed-with-capabilities'])
        self.card('c', 'loopback_connect', 'approved', 'call-2', '2026-09-09T10:00:02Z')
        self.assertFalse(self.passing(expected)['one-card-per-capability'])

    def test_verify_obeys_the_same_execution_and_capability_requirements(self):
        expected = {'executed_with': {'capabilities':['local_listen','loopback_connect'], 'program':'selfcheck.py'}}
        self.command(1,'verify-call',['local_listen','loopback_connect'],tool='verify',python='python')
        self.assertTrue(all(self.passing(expected).values()))
        for receipt in self.session['invocations']: receipt['source_verdict']='failed'
        self.assertFalse(self.passing(expected)['supplied-program-executed-with-capabilities'])
        for receipt in self.session['invocations']: receipt['source_verdict']='passed'
        self.tool_result(1)['tool_args']['capability_request'].pop('loopback_connect')
        self.assertFalse(self.passing(expected)['supplied-program-executed-with-capabilities'])

    def test_background_client_can_succeed_without_a_host_wait(self):
        expected = {'artifact_from_service': {'program': 'client.py', 'service_program': 'serve.py'}}
        self.command(1, 'serve', ['local_listen'], program='serve.py', committed='2026-09-09T10:00:00.41047Z')
        self.command(2, 'client', ['loopback_connect'], program='client.py', committed='2026-09-09T10:00:00.410470001Z')
        self.assertTrue(all(self.passing(expected).values()), self.passing(expected))
        self.receipt('client')['started_at']='2026-09-09T10:00:00.410469999Z'
        self.assertFalse(self.passing(expected)['supplied-service-started-before-client'])

    def test_background_client_needs_an_executed_success_not_just_correct_bytes(self):
        expected = {'artifact_from_service': {'program': 'client.py', 'service_program': 'serve.py'}}
        self.command(1, 'serve', ['local_listen'], program='serve.py', committed='2026-09-09T10:00:00Z')
        self.command(2, 'client', ['loopback_connect'], program='client.py')
        for column, value in [('invoked', 0), ('source_verdict', 'failed'), ('status', 'rejected')]:
            with self.subTest(column=column):
                self.receipt('client')[column]=value
                self.assertFalse(self.passing(expected)['supplied-client-succeeded'])
                self.receipt('client').update(invoked=True,source_verdict='passed',status='completed')
        self.command(3, 'rewrite', [], writes=['client.py'])
        self.assertFalse(self.passing(expected)['service-programs-unchanged'])

    def test_declared_command_cannot_fabricate_the_receipt_or_temporarily_rewrite_the_program(self):
        expected = {'executed_with': {'capabilities': ['local_listen', 'loopback_connect'], 'artifact': 'receipt.json', 'program': 'selfcheck.py'}}
        self.command(1, 'fake', ['local_listen', 'loopback_connect'], writes=['receipt.json'], program='-c "print(1)"')
        self.assertFalse(self.passing(expected)['supplied-program-executed-with-capabilities'])
        self.command(2, 'run', ['local_listen', 'loopback_connect'], writes=['receipt.json'])
        self.assertTrue(all(self.passing(expected).values()))
        self.command(3, 'rewrite', [], writes=['selfcheck.py'], program='-c "print(1)"')
        self.assertFalse(self.passing(expected)['service-program-unchanged'])

    def test_denials_and_grants_are_card_facts(self):
        self.assertFalse(self.passing({'denied': ['direct_ip']})['capability-denied'])
        self.card('d', 'direct_ip', 'rejected', 'call-3', '2026-09-09T10:00:03Z')
        self.assertTrue(self.passing({'denied': ['direct_ip']})['capability-denied'])
        self.assertFalse(self.passing({'granted': ['write_root_set']})['capability-granted'])
        self.card('w', 'write_root_set', 'approved', 'call-4', '2026-09-09T10:00:04Z')
        self.assertTrue(self.passing({'granted': ['write_root_set']})['capability-granted'])

    def test_connection_environment_does_not_invalidate_successful_program_execution(self):
        expected = {'executed_with': {'capabilities': ['local_listen', 'loopback_connect'], 'artifact': 'receipt.json', 'program': 'selfcheck.py'}}
        self.command(1, 'run', ['local_listen', 'loopback_connect'], writes=['receipt.json'])
        self.tool_result(1)['tool_args']['env']={'NO_PROXY':'127.0.0.1'}
        self.assertTrue(all(self.passing(expected).values()))

    def test_retry_boundary_is_the_matching_capability_grant(self):
        expected = {'no_retry_codes_after_grant': ['SANDBOX_TRY_LOOPBACK_CONNECT']}
        self.command(1, 'listen', ['local_listen'], program='serve.py')
        self.card('listen-card', 'local_listen', 'approved', 'listen', '2026-09-09T10:00:00Z')
        self.result(2, 'wait', 'first-connect', {}, outcome='rejected', codes=['SANDBOX_TRY_LOOPBACK_CONNECT'])
        self.assertFalse(self.passing(expected)['no-boundary-retry-after-grant'])
        self.command(3, 'connect', ['loopback_connect'], program='client.py')
        self.card('connect-card', 'loopback_connect', 'approved', 'connect', '2026-09-09T10:00:02Z')
        self.assertTrue(self.passing(expected)['no-boundary-retry-after-grant'])
        self.result(4, 'wait', 'retry', {}, outcome='rejected', codes=['SANDBOX_TRY_LOOPBACK_CONNECT'])
        self.assertFalse(self.passing(expected)['no-boundary-retry-after-grant'])


if __name__ == '__main__':
    unittest.main()

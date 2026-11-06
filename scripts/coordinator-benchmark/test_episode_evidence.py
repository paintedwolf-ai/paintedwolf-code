"""Typed evidence builders and acceptance-rule controls for episode evidence."""
import copy
import json
import pathlib
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch
import episode_evidence
from episode_evidence import verify_allowance, allowance_checks, verify_execution, final_grounding
from ledger import scripted_execution_errors, scripted_outcome


def empty_facts():
    return {'version':1,'root_session_id':'root','project_id':'project','execution':None,'allowance':None,'workers':[],
            'effects':[],'promotions':{},'snapshots':{},'verification':[],'delivered_paths':[],
            'sessions':[{'id':'root','parent_id':'','messages':[],'invocations':[],'checkpoints':[],'workflows':[],'verdicts':[]}]}


class EvidenceCase(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
        self.root=pathlib.Path(self.temp.name)
        self.facts=empty_facts()
        mock=patch.object(episode_evidence,'path_for',return_value=self.root/'facts.json')
        mock.start();self.addCleanup(mock.stop)
        self.save()

    @property
    def session(self): return self.facts['sessions'][0]

    def save(self):
        (self.root/'facts.json').write_text(json.dumps(self.facts))

    def job_by_id(self, id_): return next(j for j in self.facts['workers'] if j['id']==id_)

    def tool_result(self, ord_): return next(m['tool_result'] for m in self.session['messages'] if m['ord']==ord_)


class EpisodeExportTests(unittest.TestCase):
    def test_export_uses_selected_driver_and_binds_the_requested_root(self):
        with tempfile.TemporaryDirectory() as temporary:
            capture=pathlib.Path(temporary)
            (capture/'store.db').write_bytes(b'closed application store')
            source=capture/'source';source.mkdir()
            project=capture/'delivery';project.mkdir()
            session='00000000-0000-0000-0000-000000000001'
            facts=empty_facts();facts['root_session_id']=session;facts['sessions'][0]['id']=session
            with patch.object(episode_evidence.subprocess,'run',return_value=SimpleNamespace(stdout=json.dumps(facts))) as invoke:
                episode_evidence.export(capture,{'session_id':session,'project_dir':str(project)},source)
            arguments=invoke.call_args.args[0]
            self.assertEqual(arguments[0],str(source/'.bin/lycaon-debug'))
            self.assertEqual(arguments[-2:],['--project',str(project)])
            self.assertEqual(invoke.call_args.kwargs['cwd'],source)
            self.assertEqual(episode_evidence.read(capture,session),facts)
            with self.assertRaises(ValueError):
                episode_evidence.export(capture,{'session_id':'not-a-session'},source)

    def test_mismatched_identity_or_concurrent_store_change_never_publishes_facts(self):
        for fault in ['root','version','store','wal']:
            with self.subTest(fault=fault),tempfile.TemporaryDirectory() as temporary:
                capture=pathlib.Path(temporary);database=capture/'store.db';database.write_bytes(b'closed')
                session='00000000-0000-0000-0000-000000000001'
                facts=empty_facts();facts['root_session_id']=session
                if fault=='root':facts['root_session_id']='other'
                if fault=='version':facts['version']=99
                def invoke(*args,**kwargs):
                    if fault=='store':database.write_bytes(b'changed')
                    if fault=='wal':(capture/'store.db-wal').write_bytes(b'pending write')
                    return SimpleNamespace(stdout=json.dumps(facts))
                with patch.object(episode_evidence.subprocess,'run',side_effect=invoke):
                    with self.assertRaises(ValueError):
                        episode_evidence.export(capture,{'session_id':session},capture)
                self.assertFalse(episode_evidence.path_for(capture,session).exists())


class AllowanceEvidenceTests(EvidenceCase):
    def setUp(self):
        super().setUp()
        self.case={'session_id':'root','status':'review_required','task_allowance':{'session_id':'root','limit':2,'completed':2}}
        self.facts['allowance']=copy.deepcopy(self.case['task_allowance'])
        self.save()

    def test_last_response_can_finish_without_exhaustion(self):
        verify_allowance(self.root,self.case,2)
        self.assertTrue(allowance_checks(self.case)[0]['passed'])

    def test_next_request_requires_recorded_exhaustion(self):
        self.facts['allowance']['exhausted_attempt_id']='attempt'
        self.save()
        with self.assertRaises(ValueError):verify_allowance(self.root,self.case,2)
        self.case['task_allowance']['exhausted_attempt_id']='attempt'
        verify_allowance(self.root,self.case,2)
        self.assertFalse(allowance_checks(self.case)[0]['passed'])

    def test_count_or_policy_cannot_be_changed_by_report(self):
        for field,value in [('completed',1),('limit',3),('session_id','child')]:
            case={**self.case,'task_allowance':{**self.case['task_allowance'],field:value}}
            with self.assertRaises(ValueError): verify_allowance(self.root,case,2)


class ExecutionEvidenceTests(EvidenceCase):
    def setUp(self):
        super().setUp()
        self.message={'message_id':'message','output_id':'output','content':'Done.','model_authored':True,
                      'visible':True,'has_tool_calls':False,'grounding':{'traced':True}}
        self.case={'session_id':'root','final':'Done.','execution':{'session_id':'root','turn_id':'turn','status':'complete',
            'closeout_attempt_id':'attempt','output_id':'output','submission_ids':['submission'],'closeout':self.message}}
        self.facts['execution']=copy.deepcopy(self.case['execution'])
        self.save()

    def test_transcript_changes_do_not_move_closeout_or_grounding(self):
        self.session['messages'].append({'id':'later-host-repair','content':'different'})
        verify_execution(self.root,self.case)
        self.assertEqual(final_grounding(self.facts,'root'),{'traced':True})

    def test_report_cannot_substitute_a_handoff(self):
        with self.assertRaises(ValueError):verify_execution(self.root,{**self.case,'final':'Other'})

    def test_unadmitted_infrastructure_failure_has_no_execution(self):
        self.facts['execution']=None
        self.save()
        case = {'session_id': 'root', 'status': 'error',
                'failure': {'kind': 'provider', 'code': 'provider_unreachable', 'retryable': False}}
        self.assertFalse(verify_execution(self.root, case))
        self.assertFalse(verify_execution(self.root, {**case, 'session_id': ''}))
        with self.assertRaisesRegex(ValueError, 'no application turn'):
            verify_execution(self.root, {'status': 'review_required'})
        for changed in [{'status': 'review_required'}, {'final': 'Done'},
                        {'execution': self.case['execution']}, {'artifact_ids': ['artifact']},
                        {'failure': {'kind': 'model', 'code': 'model_failed'}},
                        {'failure': {'kind': 'provider'}}, {'failure': None}]:
            with self.subTest(changed=changed), self.assertRaisesRegex(ValueError, 'no application turn'):
                verify_execution(self.root, {**case, **changed})

    def test_infrastructure_error_does_not_hide_an_existing_execution(self):
        case = {'session_id': 'root', 'status': 'error',
                'failure': {'kind': 'provider', 'code': 'provider_unreachable'}}
        with self.assertRaisesRegex(ValueError, 'differs from its application facts'):
            verify_execution(self.root, case)

    def test_new_execution_does_not_inherit_old_grounding(self):
        self.facts['execution'].update(turn_id='next',status='running',closeout_attempt_id='',output_id='')
        self.facts['execution'].pop('closeout')
        self.save()
        self.assertEqual(final_grounding(self.facts,'root'),{})
        with self.assertRaises(ValueError):verify_execution(self.root,self.case)

    def test_host_fallback_is_never_a_model_handoff(self):
        message={**self.message,'model_authored':False}
        self.facts['execution']['closeout']=message
        self.save()
        case={**self.case,'final':'Done.','execution':{**self.case['execution'],'closeout':message}}
        verify_execution(self.root,case)
        self.assertEqual(final_grounding(self.facts,'root'),{})

    def test_host_continuation_keeps_the_current_closeout(self):
        self.facts['execution']['turn_id']='host'
        self.save()
        case={**self.case,'execution':{**self.case['execution'],'turn_id':'host'}}
        verify_execution(self.root,case)
        self.assertEqual(final_grounding(self.facts,'root'),{'traced':True})


class ReceiptTests(EvidenceCase):
    def setUp(self):
        super().setUp()
        (self.root/'worker-scripts/outcomes').mkdir(parents=True)

    def job(self,id_,child,mode,paths,status='complete'):
        self.facts['workers'].append({'id':id_,'child_session_id':child,'parent_session_id':'root','status':status,
            'merge_status':'merged','scope':{'mode':mode,'paths':paths},'created_at':'2026-09-09T10:00:00Z',
            'source_tool_call_id':'call-'+id_,'agent_type':'implementer'})
        self.save()

    def test_missing_or_failed_execution_cannot_be_a_model_measurement(self):
        self.job('job','child','write',['report.py'])
        case={'session_id':'root'}
        spec={'setup':{'policy':'scripted'}}
        self.assertEqual(scripted_execution_errors(self.root,case,spec),['job'])
        path=self.root/'worker-scripts/outcomes/job.json'
        path.write_text(json.dumps([{'job_id':'job','kind':'complete'}]))
        self.assertEqual(scripted_execution_errors(self.root,case,spec),[])
        path.write_text(json.dumps([{'job_id':'job','kind':'error','code':'HARNESS_EXECUTION_FAILED'}, {'job_id':'job','kind':'complete'}]))
        self.assertEqual(scripted_execution_errors(self.root,case,spec),['job'])
        self.assertEqual(scripted_outcome(self.root,'job')['kind'],'complete')

    def test_prepared_seed_requires_no_execution_receipt(self):
        self.job('seed','child','write',['report.py'])
        self.assertEqual(scripted_execution_errors(self.root, {'session_id':'root','prepared_overlays':{'overlays':[{'job_id':'seed'}]}}, {'setup':{'policy':'scripted'}}),[])

    def test_cancellation_before_execution_is_a_model_outcome(self):
        self.job('cancelled','child','write',['report.py'],status='cancelled')
        self.assertEqual(scripted_execution_errors(self.root, {'session_id':'root'}, {'setup':{'policy':'scripted'}}),[])

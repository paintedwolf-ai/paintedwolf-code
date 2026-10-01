import copy
from test_episode_support import EvidenceCase
from execution_evidence import verify_execution
from closeout_evidence import final_grounding


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

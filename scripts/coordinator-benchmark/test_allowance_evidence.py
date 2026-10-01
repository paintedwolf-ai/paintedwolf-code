import copy
from test_episode_support import EvidenceCase
from allowance_evidence import verify_allowance, allowance_checks


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

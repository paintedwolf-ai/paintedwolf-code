import json
from test_episode_support import EvidenceCase
from coordinator_evidence import closeout_checks, progress_checks


class CoordinatorProtocolControls(EvidenceCase):
    def setUp(self):
        super().setUp()
        self.case={'session_id':'root','preparation':{'transcript_seq':10}}

    def insert(self,**fields):
        values={'session_id':'root','role':'assistant','origin':'model','content':'Finished.','ord':20,'seq':20,**fields}
        for key in list(values):
            if key.endswith('_json'): values[key.removesuffix('_json')]=json.loads(values.pop(key))
        if values['session_id']=='root': self.session['messages'].append(values)
        if values['role']=='assistant' and values['session_id']=='root' and values['ord']>=10:
            self.facts['execution']={'status':'complete','closeout':{'model_authored':True,'grounding':values.get('grounding') or {}}}
        self.save()

    def test_closeout_requires_model_citations_in_current_root_intent(self):
        grounding = {'traced':True,'cited_evidence':[{'path':'report.py','handle':'read#2','verdict':'matched'}]}
        self.insert(role='user',origin='user',ord=10,seq=10)
        for mutation in ('none','host','untraced','wrong-path','invalid','empty-handle','prior-turn','worker','prose-only'):
            with self.subTest(mutation=mutation):
                self.session['messages']=[m for m in self.session['messages'] if m['role']=='user']
                self.facts['execution']=None
                value=json.loads(json.dumps(grounding)); extra={}
                if mutation=='host': value['host_assembled']=True
                if mutation=='untraced': value['traced']=False
                if mutation=='wrong-path': value['cited_evidence'][0]['path']='README.md'
                if mutation=='invalid': value['cited_evidence'][0]['verdict']='unverifiable'
                if mutation=='empty-handle': value['cited_evidence'][0]['handle']=''
                if mutation=='prior-turn': extra['ord']=5
                if mutation=='worker': extra['session_id']='child'
                if mutation=='prose-only': value={}
                self.insert(grounding_json=json.dumps(value),**extra)
                self.assertEqual(closeout_checks(self.root,self.case,['report.py'])[0]['passed'],mutation=='none')

    def test_progress_requires_post_entry_committed_update_and_exact_item_states(self):
        expected={'Apply labels':'done','Export report':'na'}
        invocation={'outcome':'completed','invocation':{'tool':'update_progress','invoked':True}}
        for mutation in ('none','delete','rename','annotate','false-done','pending','duplicate','preparation-only','uncommitted','prose-only'):
            with self.subTest(mutation=mutation):
                self.session['messages'].clear()
                steps=[{'label':label,'state':state} for label,state in expected.items()]
                seq=5 if mutation=='preparation-only' else 20
                result=json.loads(json.dumps(invocation))
                if mutation=='delete': steps.pop()
                if mutation=='rename': steps[1]['label']='Another task'
                if mutation=='annotate': steps[1]['label']='Export report — worker failed'
                if mutation=='false-done': steps[1]['state']='done'
                if mutation=='pending': steps[1]['state']='pending'
                if mutation=='duplicate': steps.append(steps[0])
                if mutation=='uncommitted': result['invocation']['invoked']=False
                self.insert(role='tool',origin='tool',seq=seq,tool_result_json=json.dumps(result))
                self.insert(origin='model' if mutation=='prose-only' else 'host',seq=seq+1,
                            kind='progress_complete',progress_complete_json=json.dumps({'steps':steps}))
                self.assertEqual(progress_checks(self.root,self.case,expected)[0]['passed'],mutation=='none')

    def test_host_citation_repair_does_not_hide_the_completed_answer(self):
        grounding = {'traced': True, 'cited_evidence': [
            {'path': 'report.py', 'handle': 'read#2', 'verdict': 'matched'}]}
        self.insert(role='user', origin='user', ord=10)
        self.insert(ord=20, grounding_json=json.dumps(grounding))
        self.insert(role='user', origin='host', visibility='internal', ord=21,
                    content='Retain the pinned answer and repair its citation fields.')
        self.assertTrue(closeout_checks(self.root, self.case, ['report.py'])[0]['passed'])
        self.insert(role='user', origin='user', ord=22, content='Now change the ordering.')
        self.facts['execution']['status']='running'
        self.save()
        self.assertFalse(closeout_checks(self.root, self.case, ['report.py'])[0]['passed'])

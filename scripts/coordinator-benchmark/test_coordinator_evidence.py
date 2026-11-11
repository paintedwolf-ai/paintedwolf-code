import json
from test_episode_evidence import EvidenceCase
from episode_evidence import closeout_checks, progress_checks, method_checks, cited_finding_checks


class CloseoutTests(EvidenceCase):
    def final(self,ord_,content,grounding=None,role='assistant',origin='model',kind='',boundary=None,visibility='transcript'):
        if role=='user' and (kind or boundary or visibility=='internal'): return
        self.facts['execution']={'status':'complete' if role=='assistant' else 'running',
            'closeout':{'content':content,'grounding':grounding,'model_authored':True} if role=='assistant' else None}
        self.save()

    def test_declared_method_comes_from_the_retained_grounding_audit(self):
        self.final(1, 'ask', role='user', origin='user')
        self.final(2, 'The change is complete.', {'traced': True, 'verification': {'method': 'project', 'reason': 'Full suite passed.'}})
        self.assertTrue(all(c['passed'] for c in method_checks(self.root, {'session_id': 'root'}, 'project')))
        self.assertFalse(all(c['passed'] for c in method_checks(self.root, {'session_id': 'root'}, 'blocked')))
        # Prose that still carries a trailer is not the host's record.
        self.final(3, 'Done.\n```json\n{"verification": {"method": "project", "reason": "x"}}\n```', {'traced': True})
        self.assertFalse(all(c['passed'] for c in method_checks(self.root, {'session_id': 'root'}, 'project')))
        self.final(4, 'Done.', {'traced': True, 'host_assembled': True, 'verification': {'method': 'project', 'reason': 'x'}})
        self.assertFalse(all(c['passed'] for c in method_checks(self.root, {'session_id': 'root'}, 'project')))

    def test_worker_findings_must_be_cited_and_host_resolved(self):
        self.final(1, 'ask', role='user', origin='user')
        grounding = {'traced': True, 'host_assembled': False, 'cited_evidence': [{'path': 'report.py', 'line': 2, 'verdict': 'matched'}]}
        self.final(2, 'Report.', grounding)
        findings = [{'path': 'report.py', 'line': 2}]
        self.assertTrue(all(c['passed'] for c in cited_finding_checks(self.root, {'session_id': 'root'}, findings)))
        self.assertFalse(all(c['passed'] for c in cited_finding_checks(self.root, {'session_id': 'root'}, [{'path': 'report.py', 'line': 3}])))
        self.final(3, 'Report.', {**grounding, 'host_assembled': True})
        self.assertFalse(all(c['passed'] for c in cited_finding_checks(self.root, {'session_id': 'root'}, findings)))

    def test_closeout_boundary_uses_application_user_turn_semantics(self):
        case = {'session_id': 'root'}
        findings = [{'path': 'report.py', 'line': 2}]
        self.final(1, 'Make the change.', role='user', origin='user')
        self.final(2, 'Complete.', {'traced': True, 'verification': {'method': 'project'},
                                  'cited_evidence': [{'path': 'report.py', 'line': 2, 'verdict': 'matched'}]})
        for ordinal, fields in enumerate([
                {'origin': 'host', 'visibility': 'internal'},
                {'origin': 'user', 'kind': 'user_continuation'},
                {'origin': 'host', 'kind': 'workflow_boundary'},
                {'origin': 'host', 'boundary': {'kind': 'entered'}},
        ], start=3):
            with self.subTest(fields=fields):
                self.final(ordinal, 'Continue the current task.', role='user', **fields)
                self.assertTrue(method_checks(self.root, case, 'project')[0]['passed'])
                self.assertTrue(cited_finding_checks(self.root, case, findings)[0]['passed'])
        self.final(7, 'New user task.', role='user', origin='user')
        self.assertFalse(method_checks(self.root, case, 'project')[0]['passed'])
        self.assertFalse(cited_finding_checks(self.root, case, findings)[0]['passed'])


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

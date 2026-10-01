import json
import pathlib
import tempfile
import unittest
from dispatch_graph import dependency_check_id
from orchestration_evidence import planned_dispatch_checks, planned_outcome


from test_episode_support import EvidenceCase


class LedgerCase(EvidenceCase):
    def setUp(self):
        super().setUp()
        (self.root/'worker-scripts/outcomes').mkdir(parents=True)

    def job(self,id_,child,mode,paths,status='complete',merge='merged',created='2026-09-09T10:00:00Z',call=None,outcome=None):
        self.facts['workers'].append({'id':id_,'child_session_id':child,'parent_session_id':'root','status':status,
            'merge_status':merge or '', 'scope':{'mode':mode,'paths':paths},'created_at':created,
            'source_tool_call_id':call or 'call-'+id_, 'agent_type':'implementer'})
        self.facts['sessions'].append({'id':child,'parent_id':'root','messages':[],'invocations':[],'checkpoints':[]})
        if outcome:
            (self.root/'worker-scripts/outcomes'/ (id_+'.json')).write_text(json.dumps([{'job_id':id_,**outcome}]))
        self.save()

    def message(self,ord_,calls):
        self.session['messages'].append({'id':f'm{ord_}','role':'assistant','origin':'model','ord':ord_,'tool_calls':calls})
        self.save()

    def result(self,ord_,tool,call,args=None,outcome='completed',codes=()):
        self.session['messages'].append({'id':f't{ord_}','role':'tool','origin':'tool','ord':ord_,
            'tool_result':{'tool_call_id':call,'outcome':outcome,'codes':list(codes),'tool_args':args or {},
                           'invocation':{'tool':tool,'invoked':outcome=='completed'}}})
        self.save()


class PlannedDispatchTests(LedgerCase):
    def setUp(self):
        super().setUp()
        self.spec = {'setup': {'dispatches': [
            {'label':'First', 'mode':'write', 'paths':['first.py'], 'stages':[{'kind':'complete'}]},
            {'label':'Second', 'mode':'write', 'paths':['second.py'], 'stages':[{'kind':'complete'}]},
            {'label':'Join', 'mode':'write', 'paths':['join.py'], 'stages':[{'kind':'complete'}]}]}}
        self.job('first','c1','write',['first.py'])
        self.job('second','c2','write',['second.py'])
        self.job('join','c3','write',['join.py'],created='2026-09-09T10:03:00Z')
        self.message(1,[{'name':'task','id':'call-join'}])
        self.facts['promotions']['first']='2026-09-09T10:01:00Z'
        self.facts['promotions']['second']='2026-09-09T10:02:00Z'

    def checks(self):
        self.save()
        return {c['id']:c['passed'] for c in planned_dispatch_checks(self.root, {'session_id':'root'}, self.spec,
                {'dependencies':{'Join':['First','Second']}})}

    def test_join_requires_every_integrated_prerequisite(self):
        self.assertTrue(all(self.checks().values()))
        self.facts['promotions']['second']='2026-09-09T10:04:00Z'
        self.assertTrue(self.checks()[dependency_check_id('First', 'Join')])
        self.assertFalse(self.checks()[dependency_check_id('Second', 'Join')])

    def test_later_promotion_cannot_repair_an_early_branch(self):
        self.job_by_id('join')['created_at']='2026-09-09T10:00:30Z'
        self.facts['promotions']['join']='2026-09-09T10:05:00Z'
        self.assertFalse(self.checks()[dependency_check_id('First', 'Join')])
        self.assertFalse(self.checks()[dependency_check_id('Second', 'Join')])

    def test_dependency_requires_a_distinct_child_and_a_bound_task_call(self):
        self.session['messages'].clear()
        self.assertFalse(self.checks()[dependency_check_id('First', 'Join')])
        self.message(1,[{'name':'task','id':'call-join'}])
        self.job_by_id('join')['child_session_id']='c1'
        self.assertFalse(self.checks()[dependency_check_id('First', 'Join')])

    def test_rejected_overlay_and_off_plan_dispatch_fail_independently(self):
        self.job_by_id('first')['merge_status']='rejected'
        self.assertFalse(self.checks()['planned-leg-settled-First'])
        self.job('extra','c4','write',['other.py'],status='failed',merge=None,
                 outcome={'kind':'failed','code':'HARNESS_DISPATCH_UNPLANNED'})
        self.assertFalse(self.checks()['no-off-plan-dispatch'])

    def test_failed_leg_needs_the_declared_host_outcome(self):
        self.spec['setup']['dispatches'][0]['stages'] = [{'kind':'failed','code':'FIXTURE_LEG_FAILED'}]
        self.assertEqual(planned_outcome(self.spec['setup']['dispatches'][0]),{'status':'failed','code':'FIXTURE_LEG_FAILED'})
        self.job_by_id('first').update(status='failed',merge_status='')
        path = self.root/'worker-scripts/outcomes/first.json'
        for code, expected in [('OTHER',False),('FIXTURE_LEG_FAILED',True)]:
            path.write_text(json.dumps([{'kind':'failed','code':code}]))
            self.assertEqual(self.checks()['planned-leg-settled-First'],expected)

    def test_failed_verification_is_not_an_off_plan_dispatch(self):
        path = self.root/'worker-scripts/outcomes/first.json'
        path.write_text(json.dumps([{'kind':'failed','code':'HARNESS_DELIVERY_VERIFICATION_FAILED'}]))
        self.job_by_id('first').update(status='failed',merge_status='')
        checks = self.checks()
        self.assertFalse(checks['planned-leg-settled-First'])
        self.assertTrue(checks['no-off-plan-dispatch'])

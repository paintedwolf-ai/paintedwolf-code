import json
import unittest
from orchestration_evidence import (
    decision_checks,
    dependency_check_id,
    planned_dispatch_checks,
    planned_outcome,
    question_checks,
    waves,
    worker_checks,
)
from preparation import validate_baselines
from test_episode_evidence import EvidenceCase


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


class DispatchGraphTests(unittest.TestCase):
    def setUp(self):
        self.dispatches = [{'label':label,'mode':'write'} for label in ['First','Second','Join']]

    def test_composite_check_identity_preserves_label_boundaries(self):
        self.assertNotEqual(dependency_check_id('A-to-B','C'), dependency_check_id('A','B-to-C'))

    def test_join_keeps_both_prerequisites_in_the_first_wave(self):
        result = waves(self.dispatches, {'Join':['First','Second']})
        self.assertEqual([[d['label'] for d in w] for w in result], [['First','Second'],['Join']])

    def test_undeclared_cyclic_self_and_duplicate_dependencies_are_rejected(self):
        for dependencies in [{'Other':['First']}, {'Join':['Other']}, {'Join':['Join']},
                             {'Join':['First','First']}, {'Join':[]}, {'Join':['First'],'First':['Join']}]:
            with self.subTest(dependencies=dependencies), self.assertRaises(ValueError):
                waves(self.dispatches, dependencies)

    def test_read_worker_cannot_supply_an_integration_prerequisite(self):
        self.dispatches[0]['mode'] = 'read'
        with self.assertRaises(ValueError): waves(self.dispatches, {'Join':['First']})


class PreflightAdmissionTests(unittest.TestCase):
    def test_each_admitted_project_has_its_own_baseline(self):
        value = {'admissions':[{'case':'case','project_id':'one'}, {'case':'scanner-runtime','project_id':'scan'}],
                 'baseline_checks':[{'project_id':'one'},{'project_id':'scan'}], 'scanner_health':{'project_id':'scan'}}
        validate_baselines(value, {'case'})
        for baselines in [[{'project_id':'one'}], [{'project_id':'one'},{'project_id':'one'}],
                          [{'project_id':'one'},{'project_id':'other'}]]:
            with self.subTest(baselines=baselines), self.assertRaises(ValueError):
                validate_baselines({**value,'baseline_checks':baselines}, {'case'})


class WorkerEvidenceTests(LedgerCase):
    def checks(self):
        self.save()
        return {c['id']:c['passed'] for c in worker_checks(self.root,{'id':'worker-integration','session_id':'root'},
                                                        {'writer','reader'},{'test_regression.py'})}

    def effect(self,path,branch=''):
        self.facts['effects'].append({'job_id':'implement','path':path,'branch_id':branch,'origin':'agent','entry_kind':'file','op':'write'})

    def test_integration_requires_distinct_completed_review_and_landed_code_and_test(self):
        self.job('implement','writer','write',['ranking.py','test_regression.py'])
        self.job('review','reader','read',['ranking.py'],status='running',merge='')
        self.effect('ranking.py')
        self.assertFalse(self.checks()['worker-review'])
        self.job_by_id('review')['status']='complete'
        self.assertTrue(self.checks()['worker-review'])
        self.assertFalse(self.checks()['worker-contribution'])
        self.effect('test_regression.py',branch='overlay')
        self.assertFalse(self.checks()['worker-contribution'])
        self.facts['effects'][-1]['branch_id']=''
        self.assertTrue(all(self.checks().values()))
        self.job_by_id('review')['child_session_id']='writer'
        self.assertFalse(self.checks()['independent-workers'])
        self.job_by_id('implement')['merge_status']='rejected'
        self.assertFalse(self.checks()['worker-contribution'])

    def test_read_scope_and_complete_return_are_required(self):
        self.job('review','reader','read',['ranking.py'],status='failed',merge='')
        self.assertFalse(self.checks()['worker-review'])
        self.job_by_id('review')['status']='complete'
        self.assertTrue(self.checks()['worker-review'])
        self.job_by_id('review')['scope']['mode']='write'
        self.assertFalse(self.checks()['worker-review'])


class DecisionTests(LedgerCase):
    def setUp(self):
        super().setUp()
        self.spec = {'setup': {'dispatches': [
            {'label':'Recent', 'mode':'write', 'paths':['report.py']},
            {'label':'Archive', 'mode':'write', 'paths':['archive.py']}]}}
        self.expected = {'Recent': {'option':'3'}, 'Archive': {'option':'8'}}
        self.job('recent', 'c1', 'write', ['report.py'])
        self.job('archive', 'c2', 'write', ['archive.py'])

    def answer(self, ord_, job, option, resolved_by='coordinator', outcome='completed'):
        result = {'tool_call_id': f'call-{ord_}', 'outcome': outcome,
                  'tool_args': {'job_id': job, 'option': option, 'resolved_by': resolved_by},
                  'invocation': {'tool': 'answer_decision', 'invoked': True},
                  'content': json.dumps({'status':'resumed', 'job_id':job, 'option':option, 'resolved_by':resolved_by})}
        self.session['messages'].append({'id':f't{ord_}','role':'tool','origin':'tool','ord':ord_,'tool_result':result})
        self.save()

    def checks(self):
        self.save()
        return {c['id']:c['passed'] for c in decision_checks(self.root, {'session_id':'root'}, self.spec, self.expected)}

    def test_distinct_answers_are_bound_to_jobs_in_any_order(self):
        self.answer(1,'archive','8')
        self.answer(2,'recent','3')
        self.assertTrue(all(self.checks().values()))

    def test_swapping_correct_options_between_workers_fails_both(self):
        self.answer(1,'archive','3')
        self.answer(2,'recent','8')
        self.assertFalse(self.checks()['decision-answered-Recent'])
        self.assertFalse(self.checks()['decision-answered-Archive'])

    def test_repeating_one_answer_cannot_cover_another_worker(self):
        self.answer(1,'recent','3')
        self.answer(2,'recent','3')
        self.assertFalse(self.checks()['decision-answered-Recent'])
        self.assertFalse(self.checks()['decision-answered-Archive'])

    def test_wrong_owner_rejected_answer_or_redispatch_cannot_pass(self):
        for failure in ['owner','rejected','redispatch']:
            with self.subTest(failure=failure):
                self.session['messages'].clear()
                self.answer(1,'archive','8')
                self.answer(2,'recent','3',resolved_by='user' if failure=='owner' else 'coordinator',
                            outcome='rejected' if failure=='rejected' else 'completed')
                if failure=='redispatch': self.job('again','c3','write',['report.py'])
                self.assertFalse(self.checks()['decision-answered-Recent'])


class QuestionTests(LedgerCase):
    def test_only_questions_in_the_coordinator_session_are_counted(self):
        case = {'session_id': 'root'}
        self.assertTrue(question_checks(self.root, case)[0]['passed'])
        self.message(1, [{'name': 'ask_user', 'args': {'prompt': 'Any preference?'}}])
        self.assertFalse(question_checks(self.root, case)[0]['passed'])
        self.facts['sessions'].append({**self.session,'id':'other'})
        self.session['messages']=[]
        self.save()
        self.assertTrue(question_checks(self.root, case)[0]['passed'])


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

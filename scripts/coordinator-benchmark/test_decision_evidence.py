import json
import unittest
from decision_evidence import decision_checks, question_checks
from test_orchestration_evidence import LedgerCase


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

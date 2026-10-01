import json
import unittest
from closeout_evidence import method_checks, cited_finding_checks
from test_orchestration_evidence import LedgerCase


class CloseoutTests(LedgerCase):
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

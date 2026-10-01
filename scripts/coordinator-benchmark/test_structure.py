import json
import unittest
import structure
from test_orchestration_evidence import LedgerCase


class StructureTests(LedgerCase):
    def test_counts_come_from_the_ledger_and_the_operator_record(self):
        self.message(1, [{'name': 'task', 'args': {}}, {'name': 'task', 'args': {}}])
        self.message(2, [{'name': 'wait', 'args': {}}])
        self.message(3, [{'name': 'promote_overlay', 'args': {}}])
        self.result(4,'command','cmd',outcome='rejected',codes=['COMMAND_NOT_ARGV'])
        self.session['checkpoints'].append({'checkpoint_id':'c','kind':'tool_approval','status':'approved',
            'issued_at':'2026-09-09T10:00:00Z','tool_approval':{'tool_call_id':'cmd','plan':{'subject':{'kind':'local_listen'}}}})
        self.job('a', 'c1', 'write', ['a.py'])
        case = {'session_id': 'root',
                'automatic_responses': [{'kind': 'approval_rejected'}, {'kind': 'fixture_rule_denied'}, {'kind': 'fixture_rule_denied'}]}
        observed = structure.observe(self.root, case)
        self.assertEqual(observed['tool_calls'], 4)
        self.assertEqual(observed['tool_calls_by_name'], {'promote_overlay': 1, 'task': 2, 'wait': 1})
        self.assertEqual(observed['rejected_by_family'], {'COMMAND_NOT_ARGV': 1})
        self.assertEqual(observed['approval_cards_by_subject'], {'local_listen': 1})
        self.assertEqual(observed['interventions'], 2)
        self.assertTrue({'prompt_tokens', 'completion_tokens', 'model_calls'}.isdisjoint(observed))
        summary = structure.summarize([observed, {**observed, 'tool_calls': 2}])
        self.assertEqual(summary['tool_calls'], 3)
        self.assertEqual(summary['trials'], 2)
        self.assertIsNone(structure.summarize([]))

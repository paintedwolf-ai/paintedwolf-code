"""Qualification approvals bind exact calls and never infer permission from card prose."""
import copy
from types import SimpleNamespace
import unittest
from unittest.mock import Mock

from linux_release.installed_cases import approval

SESSION = 'ab43c1ae-cffd-4c40-adc1-253d8232217a'
CHECKPOINT = '0cd07e3a-75ae-402a-a1ae-afeb1f08c207'
ARGS = {'command': 'fixed fixture', 'capability_request': {'host_execution': True}}
CALL = {'id': 'host_minted_call', 'name': 'command', 'args': ARGS}
PLAN = {'id': CHECKPOINT, 'session_id': SESSION, 'kind': 'tool_approval', 'status': 'pending',
        'tool_approval': {'tool_call_id': CALL['id'], 'plan': {
            'stage': 'pre_spawn', 'subject': {'kind': 'host_execution'}, 'options': [
                {'id': 'opaque_once', 'kind': 'current_action', 'rung': 'once',
                 'decision_action': 'approve'}]}}}


class InstalledApprovalTests(unittest.TestCase):
    def test_only_exact_transcript_call_and_typed_one_shot_option_are_selectable(self):
        self.assertEqual(approval.selection(PLAN, SESSION, [CALL], ARGS), (CHECKPOINT, 'opaque_once'))
        for mutation in ('command', 'id', 'name', 'malformed', 'duplicate'):
            calls = copy.deepcopy([CALL])
            if mutation == 'command':
                calls[0]['args']['command'] = 'different command'
            elif mutation == 'id':
                calls[0]['id'] = 'provider_id'
            elif mutation == 'name':
                calls[0]['name'] = 'other_tool'
            elif mutation == 'malformed':
                calls[0]['args_malformed'] = True
            else:
                calls.append(copy.deepcopy(calls[0]))
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                approval.selection(PLAN, SESSION, calls, ARGS)

    def test_labels_cannot_authorize_disabled_durable_joined_or_presence_plans(self):
        for mutation in ('subject', 'disabled', 'rung', 'held', 'joined', 'option_duplicate', 'session'):
            row = copy.deepcopy(PLAN)
            payload = row['tool_approval']
            plan = payload['plan']
            plan['subject']['title'] = 'Allow host execution once'
            if mutation == 'subject':
                plan['subject']['kind'] = 'action'
            elif mutation == 'disabled':
                plan['options'][0]['disabled'] = True
            elif mutation == 'rung':
                plan['options'][0]['rung'] = 'device'
            elif mutation == 'held':
                plan['held_release'] = {'anything': 'requires presence'}
            elif mutation == 'joined':
                payload['joined_tool_call_ids'] = ['foreign']
            elif mutation == 'option_duplicate':
                plan['options'].append(copy.deepcopy(plan['options'][0]))
            else:
                row['session_id'] = CHECKPOINT
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                approval.selection(row, SESSION, [CALL], ARGS)

    def test_resolution_posts_exact_opaque_option_and_requires_matching_approval_receipt(self):
        for receipt in ({'id': CHECKPOINT, 'session_id': SESSION, 'kind': 'tool_approval', 'status': 'approved'},
                        {'id': CHECKPOINT, 'session_id': SESSION, 'kind': 'tool_approval', 'status': 'rejected'}):
            app = SimpleNamespace(request=Mock(side_effect=[
                {'status': 200, 'body': {'checkpoints': [PLAN]}},
                {'status': 200, 'body': {'messages': [{'tool_calls': [CALL]}]}},
                {'status': 200, 'body': receipt}]))
            turn = SimpleNamespace(app=app, session_id=SESSION, observations={})
            if receipt['status'] == 'approved':
                approval.approve_host_execution(turn, ARGS)
                self.assertEqual(turn.observations['host_execution_approval']['status'], 'approved')
            else:
                with self.assertRaises(ValueError):
                    approval.approve_host_execution(turn, ARGS)
                self.assertEqual(turn.observations, {})
            app.request.assert_called_with('POST', f'/v1/sessions/{SESSION}/checkpoints/{CHECKPOINT}',
                {'kind': 'tool_approval', 'action': 'approve', 'option_id': 'opaque_once'})

    def test_unbound_pending_plan_is_never_resolved(self):
        app = SimpleNamespace(request=Mock(side_effect=[
            {'status': 200, 'body': {'checkpoints': [PLAN]}},
            {'status': 200, 'body': {'messages': [{'tool_calls': []}]}}]))
        with self.assertRaises(ValueError):
            approval.approve_host_execution(SimpleNamespace(app=app, session_id=SESSION, observations={}), ARGS)
        self.assertTrue(all(call.args[0] == 'GET' for call in app.request.call_args_list))

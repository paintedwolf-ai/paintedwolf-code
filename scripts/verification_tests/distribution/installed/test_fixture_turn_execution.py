"""The common fixed GUI turn requires both projected and durable receipt identities."""
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.installed_cases import fixture_turn as owner

PROJECT = '10000000-0000-4000-8000-000000000001'
SESSION = '10000000-0000-4000-8000-000000000002'
OUTPUT = '10000000-0000-4000-8000-000000000003'
MESSAGE = '10000000-0000-4000-8000-000000000004'


class FixtureTurnTests(unittest.TestCase):
    def test_valid_tool_receipts_survive_later_reply_refusal_or_timeout_without_success(self):
        session = {'id': SESSION, 'provider_id': 'fixture', 'model': owner.MODEL, 'status': 'idle'}
        acquisition = SimpleNamespace(tool='page_open', completed=True,
            witness={'tool_call_id': 'call_1234abcd', 'invocation_id': OUTPUT},
            tool_witness={'tool_call_id': 'call_abcd1234', 'invocation_id': OUTPUT})
        proof = {'message_id': MESSAGE, 'invocation_id': OUTPUT}
        for mode in ('refusal', 'timeout', 'session-error'):
            with self.subTest(mode=mode):
                events, ticks = [], iter((0, 0, 100))
                def receipt(app, project, session, tool, call_id, **kwargs):
                    events.append(tool)
                    return {**proof, 'tool': tool}
                def reply(*args):
                    events.append('reply')
                    if mode == 'refusal':
                        raise ValueError('PRIVATE diagnostic must not enter observations')
                    return None
                selected = {**session, 'status': 'error' if mode == 'session-error' else 'idle'}
                with patch.object(owner, 'project_for_owned_root', return_value={'project_id': PROJECT}), patch.object(
                        owner, 'sessions_for_project', return_value=[selected]), patch.object(
                        owner, 'committed_fixture_tool', side_effect=receipt), patch.object(
                        owner, 'committed_reply', side_effect=reply), patch.object(owner, 'time',
                        SimpleNamespace(monotonic=lambda: next(ticks), sleep=lambda _: None)):
                    with self.assertRaises(ValueError) as refused:
                        owner.await_fixture_reply(None, None, 'fixture', acquisition)
                observed = refused.exception.observations['fixture_tool_receipts']
                self.assertEqual(observed['tool_invocation'], {**proof, 'tool': 'page_open'})
                self.assertEqual(events[:2], ['request_tools', 'page_open'])
                self.assertNotIn('PRIVATE', str(observed))
                self.assertNotIn('fixed_reply_verified', observed)

    def test_settled_reply_requires_the_exact_successful_tool_call(self):
        for target in ('command', 'page_open'):
            with self.subTest(target=target):
                self.assert_tool(target)

    def assert_tool(self, target):
        session = {'id': SESSION, 'provider_id': 'fixture', 'model': owner.MODEL, 'status': 'idle'}
        proof = {'message_id': MESSAGE, 'invocation_id': OUTPUT}
        acquisition = SimpleNamespace(tool=target, completed=True, witness={'tool_call_id': 'call_1234abcd', 'invocation_id': OUTPUT},
            tool_witness={'tool_call_id': 'call_abcd1234', 'invocation_id': OUTPUT})
        with patch.object(owner, 'project_for_owned_root', return_value={'project_id': PROJECT}), patch.object(
                owner, 'sessions_for_project', return_value=[session]), patch.object(
                owner, 'committed_reply', return_value={'session_id': SESSION}), patch.object(
                owner, 'committed_fixture_tool', return_value=proof) as observed:
            self.assertEqual(owner.await_fixture_reply(None, None, 'fixture', acquisition)['tool_message_id'], MESSAGE)
            for missing in ('request_tools', target):
                observed.side_effect = lambda app, project, session, tool, call_id, **kwargs: None if tool == missing else proof
                with self.assertRaisesRegex(ValueError, 'did not complete'):
                    owner.await_fixture_reply(None, None, 'fixture', acquisition)
            observed.side_effect = None
            observed.return_value = {**proof, 'invocation_id': PROJECT}
            with self.assertRaisesRegex(ValueError, 'differs from the projected'):
                owner.await_fixture_reply(None, None, 'fixture', acquisition)


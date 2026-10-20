"""Installed tool scenarios require accepted public API operations and actual invocation facts."""
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
import uuid

from linux_release.installed_cases.workflow import ToolTurn, accepted, identity
from linux_release.installed_cases.security_boundary import successful_invocation


class InstalledWorkflowTests(unittest.TestCase):
    def test_refusal_and_invalid_resource_ids_are_not_success(self):
        app = SimpleNamespace(request=lambda *args: {'status': 400, 'body': {'error': 'fixture'}})
        with self.assertRaises(ValueError):
            accepted(app, 'POST', '/v1/providers', {}, {201})
        for value in ['foreign', None, True]:
            with self.assertRaises((ValueError, TypeError, AttributeError)):
                identity({'id': value})
        resource = str(uuid.uuid4())
        self.assertEqual(identity({'id': resource}), resource)

    def test_prepared_workspace_tool_result_and_owned_abort_are_required(self):
        provider = SimpleNamespace(base_url='http://127.0.0.1:12345/v1')
        project_id, session_id = str(uuid.uuid4()), str(uuid.uuid4())
        calls = []
        with tempfile.TemporaryDirectory() as directory:
            def request(method, path, payload):
                calls.append((method, path, payload))
                if path == '/v1/providers':
                    return {'status': 201, 'body': {}}
                if path == '/v1/projects':
                    return {'status': 201, 'body': {'id': project_id}}
                if path == '/v1/sessions':
                    return {'status': 202, 'body': {'id': session_id}}
                if path.endswith('/prompts'):
                    return {'status': 202, 'body': {}}
                if '/messages?' in path:
                    return {'status': 200, 'body': {'messages': [{'tool_result': {
                        'tool': 'command', 'outcome': 'completed', 'content': 'private output'}}]}}
                if path.endswith('/abort'):
                    return {'status': 200, 'body': {}}
                return {'status': 200, 'body': {'status': 'idle', 'workspace_path': directory}}
            with ToolTurn(SimpleNamespace(request=request), provider, Path(directory)) as turn:
                self.assertEqual(turn.workspace, Path(directory))
                turn.submit()
                self.assertEqual(turn.wait()[0]['tool'], 'command')
                self.assertNotIn('private', str(turn.observations))
            self.assertEqual(calls[-1][:2], ('POST', '/v1/sessions/' + session_id + '/abort'))
            self.assertEqual(calls[0][2]['kind'], 'openai-compatible')
            self.assertFalse(calls[0][2]['requires_api_key'])

    def test_completed_prose_or_uninvoked_receipt_cannot_establish_execution(self):
        self.assertFalse(successful_invocation([{'tool': 'command', 'content': 'completed'}]))
        row = {'tool': 'command', 'outcome': 'completed', 'invocation': {
            'tool': 'command', 'invoked': True, 'status': 'completed'}}
        self.assertTrue(successful_invocation([row]))
        self.assertFalse(successful_invocation([row, row]))
        row['invocation']['invoked'] = False
        self.assertFalse(successful_invocation([row]))

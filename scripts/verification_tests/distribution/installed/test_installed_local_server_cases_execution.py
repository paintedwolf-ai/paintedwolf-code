"""Loopback cases run owned servers and judge refusal or narrowly approved access by their requests."""
import json
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import browser_network
from linux_release.installed_cases.records import CaseUnavailable

SESSION = '11111111-1111-4111-8111-111111111111'
CHECKPOINT = '22222222-2222-4222-8222-222222222222'
BINDING = {'binding': True}


def fetch(port: int, path: str = '/qualification') -> int:
    connection = browser_network.HTTPConnection('127.0.0.1', port, timeout=5)
    try:
        connection.request('GET', path)
        response = connection.getresponse()
        response.read()
        return response.status
    finally:
        connection.close()


class LocalServerBoundsTests(unittest.TestCase):
    def test_request_budget_long_paths_and_failed_positive_control_are_refused(self):
        with browser_network.LocalServer('public fixture') as server:
            self.assertEqual(fetch(server.port, '/' + 'q' * 4096), 429)
            self.assertEqual(server.requests, [])
            server.requests.extend(['/warm'] * 64)
            self.assertEqual(fetch(server.port), 429)
            with self.assertRaises(ValueError):
                server.baseline()
            self.assertEqual(len(server.requests), 64)

    def test_a_server_thread_that_outlives_shutdown_is_a_fixture_failure(self):
        server = browser_network.LocalServer('public fixture')
        server.__enter__()
        real = server.thread
        server.thread = SimpleNamespace(join=lambda timeout: None, is_alive=lambda: True)
        with self.assertRaises(ValueError):
            server.__exit__(None, None, None)
        real.join(timeout=5)
        self.assertFalse(real.is_alive())


def checkpoint(options=None):
    return {'id': CHECKPOINT, 'session_id': SESSION, 'kind': 'tool_approval', 'status': 'pending',
            'tool_approval': {'tool_call_id': 'fixture-call', 'plan': {'stage': 'pre_spawn',
                'subject': {'kind': 'loopback_connect'}, 'options': [{'id': 'host-minted-once',
                    'kind': 'current_action', 'rung': 'once', 'decision_action': 'approve'}] if options is None else options}}}


class ApprovalTests(unittest.TestCase):
    ARGS = {'url': 'http://127.0.0.1:12345/qualification', 'capability_request': {'loopback_connect': {'ports': [12345]}}}

    def api(self, pending, messages=None, resolved=None):
        calls = []

        def accepted(app, method, path, payload, statuses):
            calls.append((method, path, payload))
            if method == 'GET' and 'checkpoints?status=pending' in path:
                return {'checkpoints': pending.pop(0)}
            if method == 'GET' and '/messages?' in path:
                return {'messages': [{'tool_calls': [{'id': 'fixture-call', 'name': 'page_open', 'args': self.ARGS}]}]
                        if messages is None else messages}
            return {'id': CHECKPOINT, 'session_id': SESSION, 'kind': 'tool_approval', 'status': 'approved'} if resolved is None else resolved
        return calls, patch.object(browser_network, 'accepted', side_effect=accepted)

    def test_the_first_pending_checkpoint_is_approved_through_its_one_shot_option(self):
        turn = SimpleNamespace(app='app', session_id=SESSION)
        calls, api = self.api([[], [checkpoint()]])
        with api:
            self.assertEqual(browser_network.approve(turn, self.ARGS),
                             {'checkpoint_id': CHECKPOINT, 'option_id': 'host-minted-once', 'status': 'approved'})
        self.assertEqual([call[0] for call in calls], ['GET', 'GET', 'GET', 'POST'])
        self.assertEqual(calls[-1], ('POST', f'/v1/sessions/{SESSION}/checkpoints/{CHECKPOINT}',
                                     {'kind': 'tool_approval', 'action': 'approve', 'option_id': 'host-minted-once'}))

    def test_malformed_ambiguous_or_unresolved_approvals_are_refused(self):
        turn = SimpleNamespace(app='app', session_id=SESSION)
        refusals = [self.api([{'checkpoints': 'none'}]), self.api([[checkpoint(), checkpoint()]]),
                    self.api([[checkpoint()]], messages='prose'), self.api([[checkpoint()]], messages=[{'tool_calls': 'none'}]),
                    self.api([[checkpoint(options='none')]]),
                    self.api([[checkpoint()]], resolved={'id': CHECKPOINT, 'session_id': SESSION, 'kind': 'tool_approval', 'status': 'pending'})]
        for index, (calls, api) in enumerate(refusals):
            with self.subTest(index=index), api, self.assertRaises(ValueError):
                browser_network.approve(turn, self.ARGS)

    def test_no_reviewable_approval_within_the_window_leaves_the_case_unverified(self):
        turn = SimpleNamespace(app='app', session_id=SESSION)
        clock = SimpleNamespace(now=0.0)

        def monotonic():
            clock.now += 16.0
            return clock.now
        calls, api = self.api([[], []])
        with api, patch.object(browser_network, 'time', SimpleNamespace(monotonic=monotonic, sleep=lambda _: None)), \
                self.assertRaises(CaseUnavailable):
            browser_network.approve(turn, self.ARGS)
        self.assertEqual(len(calls), 1)


class Installed:
    """Patch the installed app, provider fixture, turn, approval and settle wait."""

    def __init__(self, wait, observations=None):
        self.wait = wait
        self.app = MagicMock()
        self.app.observations = {'capture_complete': True} if observations is None else observations
        self.app.streams.return_value = {'stdout': 'engine'}
        self.turn = MagicMock()
        self.turn.session_id = SESSION
        self.turn.observations = {'turn': 'facts'}
        self.turn.wait.side_effect = lambda timeout: self.wait(self.arguments())
        self.provider = MagicMock()
        self.provider.observations = {'provider': 'facts'}

    def arguments(self) -> dict:
        return self.mocks[1].call_args.args[0][0]['arguments']

    def __enter__(self):
        app_manager = MagicMock()
        app_manager.__enter__.return_value = self.app
        provider_manager = MagicMock()
        provider_manager.__enter__.return_value = self.provider
        turn_manager = MagicMock()
        turn_manager.__enter__.return_value = self.turn
        self.patches = [patch.object(browser_network, 'InstalledApp', MagicMock(return_value=app_manager)),
                        patch.object(browser_network, 'ProviderFixture', MagicMock(return_value=provider_manager)),
                        patch.object(browser_network, 'ToolTurn', MagicMock(return_value=turn_manager)),
                        patch.object(browser_network, 'approve', return_value={'status': 'approved'}),
                        patch.object(browser_network, 'wait_settled')]
        self.mocks = [item.start() for item in self.patches]
        return self

    def __exit__(self, *_):
        for item in self.patches:
            item.stop()


def refusal(arguments: dict) -> list[dict]:
    return [{'tool': 'page_open', 'tool_call_id': 'fixture-call', 'tool_args': arguments, 'outcome': 'rejected',
             'codes': ['SANDBOX_TRY_LOOPBACK_CONNECT'], 'invocation': {'tool': 'page_open', 'status': 'rejected', 'invoked': True}}]


def page(final_url: str) -> list[dict]:
    return [{'tool': 'page_open', 'outcome': 'completed', 'invocation': {'tool': 'page_open', 'invoked': True, 'status': 'completed'},
             'content': json.dumps({'id': 'page_host_1', 'live_pages': ['page_host_1'], 'final_url': final_url})}]


class DeniedLocalServicesTests(unittest.TestCase):
    def test_a_refused_page_open_that_never_reaches_the_server_passes(self):
        with Installed(lambda arguments: refusal(arguments)) as installed:
            status, facts, streams = browser_network.denied_local_services(BINDING)
        self.assertEqual(status, 'passed')
        self.assertEqual(streams, {'stdout': 'engine'})
        self.assertEqual(facts['server_requests'], [])
        self.assertTrue(facts['unconfined_server_reachable'])
        self.assertEqual(facts['refusal']['codes'], ['SANDBOX_TRY_LOOPBACK_CONNECT'])
        self.assertEqual(installed.arguments()['url'], facts['refusal']['tool_args']['url'])
        self.assertTrue(installed.arguments()['url'].startswith('http://127.0.0.1:'))
        installed.mocks[3].assert_not_called()

    def test_any_request_reaching_the_server_fails_the_case(self):
        def leaked(arguments):
            port = int(arguments['url'].split(':')[2].split('/')[0])
            fetch(port)
            return refusal(arguments)
        with Installed(leaked):
            status, facts, _ = browser_network.denied_local_services(BINDING)
        self.assertEqual(status, 'failed')
        self.assertEqual(facts['server_requests'], ['/qualification'])
        with Installed(lambda arguments: refusal(arguments), observations={'capture_complete': True, 'output_limit': True}):
            status, _, _ = browser_network.denied_local_services(BINDING)
        self.assertEqual(status, 'failed')


class AllowedLocalServerTests(unittest.TestCase):
    def test_the_approved_server_is_fetched_and_the_sibling_stays_untouched(self):
        def served(arguments):
            port = arguments['capability_request']['loopback_connect']['ports'][0]
            fetch(port)
            return page(arguments['url'])
        with Installed(served) as installed:
            status, facts, streams = browser_network.allowed_local_server(BINDING)
        self.assertEqual(status, 'passed')
        self.assertEqual(streams, {'stdout': 'engine'})
        self.assertEqual(facts['approval'], {'status': 'approved'})
        self.assertEqual(facts['approved_server_requests'], ['/qualification'])
        self.assertEqual(facts['unapproved_server_requests'], [])
        self.assertEqual(facts['requested_ports'], [int(installed.arguments()['url'].split(':')[2].split('/')[0])])
        self.assertNotEqual(facts['unapproved_sibling_port'], facts['requested_ports'][0])
        installed.mocks[3].assert_called_once_with(installed.turn, installed.arguments())
        installed.mocks[4].assert_called_once_with(installed.app, SESSION)

    def test_landing_elsewhere_or_touching_the_sibling_fails(self):
        with Installed(lambda arguments: page('http://127.0.0.1:1/qualification')):
            status, facts, _ = browser_network.allowed_local_server(BINDING)
        self.assertEqual(status, 'failed')
        self.assertEqual(facts['approved_server_requests'], [])

        servers = []
        real = browser_network.LocalServer

        def sibling(arguments):
            fetch(arguments['capability_request']['loopback_connect']['ports'][0])
            fetch(servers[0].port)
            return page(arguments['url'])
        with Installed(sibling), patch.object(browser_network, 'LocalServer',
                                              side_effect=lambda body: servers.append(real(body)) or servers[-1]):
            status, facts, _ = browser_network.allowed_local_server(BINDING)
        self.assertEqual(status, 'failed')
        self.assertEqual(len(servers), 2)
        self.assertEqual(facts['unapproved_sibling_port'], servers[0].port)
        self.assertEqual(facts['unapproved_server_requests'], ['/qualification'])


if __name__ == '__main__':
    unittest.main()

"""Protected browser turns use public GUI input and retain failures independently of confinement."""
from contextlib import ExitStack
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, Mock, patch

from linux_release.installed_cases import browser
from linux_release.installed_input import controller, protocol

MESSAGE = '10000000-0000-4000-8000-000000000001'
RECEIPT = '10000000-0000-4000-8000-000000000002'
ENGINE = {'pid': 10, 'parent_pid': 5, 'start_time_ticks': 30}
LAUNCH = {'pid': 5, 'parent_pid': 1, 'start_time_ticks': 20}


def turn():
    return {'tool_invocation': {'message_id': MESSAGE, 'invocation_id': RECEIPT,
        'tool_call_id': 'call_1234abcd', 'tool': 'page_open', 'owner': 'browser',
        'status': 'completed', 'invoked': True, 'arguments': {'path': 'fixture.html'},
        'payload': {'id': 'page_owned', 'live_pages': ['page_owned'], 'max_pages': 4}}}


class BrowserGuiTests(unittest.TestCase):
    def run_case(self, home, *, observe_error=None, launch_error=None, remaining=False):
        app = SimpleNamespace(observations={'engine_identity': ENGINE, 'launch_identity': LAUNCH},
            launch=Mock(side_effect=launch_error), wait_ready=Mock(), stop=Mock(),
            streams=Mock(return_value={'stdout': b'owned app output'}))
        app.stop.side_effect = lambda: app.observations.update(capture_complete=True)
        fixture = MagicMock()
        fixture.__enter__.return_value = fixture
        fixture.observations = [{'completed': True}]
        fixture.discovery_observations = fixture.transport_observations = []
        fixture.transport_clipped = False
        fixture.acquisition = SimpleNamespace(tool='page_open')
        stack = ExitStack(); self.addCleanup(stack.close)
        stack.enter_context(patch.object(browser.Path, 'home', return_value=home))
        constructor = stack.enter_context(patch.object(browser, 'InstalledApp', return_value=app))
        gui = stack.enter_context(patch.object(browser.ProviderFixture, 'gui', return_value=fixture))
        for name, value in [('protected_launcher', {'path': 'bound bootstrap'}),
                ('load_provider_recovery', None), ('interactive_window', {'owned': True}),
                ('configure_provider', {'provider_id': 'fixture'}),
                ('run', {'exit_code': 0, 'timed_out': False, 'output_limit': False}),
                ('namespace_observer', {'mnt': 1}), ('validate', [{'pid': 12, 'start_ticks': '40'}])]:
            stack.enter_context(patch.object(browser, name, return_value=value))
        submitted = stack.enter_context(patch.object(browser, 'submit_project_turn'))
        awaited = stack.enter_context(patch.object(browser, 'await_fixture_reply', return_value=turn()))
        observed = stack.enter_context(patch.object(browser, 'observe', side_effect=observe_error,
                                                   return_value={'selected': True}))
        cleaned = stack.enter_context(patch.object(browser, 'cleanup'))
        stack.enter_context(patch.object(browser, 'state', return_value='S' if remaining else None))
        return app, gui, constructor, submitted, awaited, observed, cleaned

    def test_normal_gui_turn_retains_typed_page_and_declared_parent_limitation(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            app, gui, constructor, submitted, awaited, _, cleaned = self.run_case(home)
            status, facts, streams = browser.browser_sandbox({})
            self.assertEqual(status, 'unverified')
            self.assertFalse(facts['protected_browser_ancestry_verified'])
            self.assertEqual(facts['reason'], 'protected-browser-observer-dependency-not-reconciled')
            self.assertEqual(facts['opened']['page']['id'], 'page_owned')
            self.assertEqual(constructor.call_args.args, ({'launcher': {'path': 'bound bootstrap'}},))
            self.assertEqual(gui.call_args.kwargs['acquire'], 'page_open')
            self.assertEqual(gui.call_args.args[0], [{'name': 'page_open', 'arguments': {'path': 'fixture.html'}}])
            app.launch.assert_called_once(); app.wait_ready.assert_called_once(); app.stop.assert_called_once()
            submitted.assert_called_once_with(app, operation='browser-fixture-prompt-v1')
            self.assertEqual(awaited.call_args.args[2], 'fixture')
            self.assertEqual((Path(facts['project_root']) / 'fixture.html').read_text(), browser.HTML)
            self.assertEqual(streams['stdout'], b'owned app output')
            cleaned.assert_called_once_with([{'pid': 12, 'start_ticks': '40'}])

    def test_successful_page_is_preserved_when_kernel_observation_refuses(self):
        with tempfile.TemporaryDirectory() as directory:
            app, _, _, _, _, _, _ = self.run_case(Path(directory), observe_error=ValueError('kernel refused'))
            with self.assertRaises(browser.CaseFailed) as refused:
                browser.browser_sandbox({})
            self.assertEqual(refused.exception.observations['opened']['page']['id'], 'page_owned')
            self.assertEqual(refused.exception.observations['failure_type'], 'ValueError')
            self.assertTrue(refused.exception.observations['launch']['capture_complete'])
            self.assertEqual(refused.exception.streams['stdout'], b'owned app output')
            app.stop.assert_called_once()

    def test_partial_launch_and_remaining_processes_do_not_become_success(self):
        with tempfile.TemporaryDirectory() as directory:
            app, _, _, submitted, _, _, _ = self.run_case(Path(directory), launch_error=ValueError('launch refused'))
            with self.assertRaises(browser.CaseFailed) as refused:
                browser.browser_sandbox({})
            self.assertIn('launch', refused.exception.observations)
            app.stop.assert_called_once(); app.wait_ready.assert_not_called(); submitted.assert_not_called()
        with tempfile.TemporaryDirectory() as directory:
            self.run_case(Path(directory), remaining=True)
            status, facts, _ = browser.browser_sandbox({})
            self.assertEqual(status, 'failed')
            self.assertEqual(facts['browser_processes_remaining_after_stop'], [{'pid': 12, 'start_ticks': '40'}])

    def test_browser_prompt_is_closed_and_independent_host_records_use_its_case(self):
        sequence = protocol.steps('browser-fixture-prompt-v1')
        self.assertEqual(sequence[0], ('ctrl', 'a'))
        self.assertNotIn(('ret',), sequence)
        with self.assertRaises(ValueError):
            protocol.steps('browser-fixture-prompt-v1-arbitrary')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / 'leaf/evidence/browser/browser-sandbox-composition/execution.json'
            path.parent.mkdir(parents=True)
            operations = ['provider-name-v1', 'provider-endpoint-v1-12345',
                          'provider-fixture-credential-v1', 'browser-fixture-prompt-v1']
            records = [{'operation': operation} for operation in operations]
            observations = {'launch': {'fixed_input': records[:3]}, 'native_input': records[3]}
            path.write_text(json.dumps({'observations': observations}))
            retained, declared = controller.delivery_records(root, {}, 'owned')
            self.assertEqual(retained, records)
            self.assertEqual(declared, operations)
            observations['launch']['fixed_input'] = records + records
            path.write_text(json.dumps({'observations': observations}))
            with self.assertRaises(ValueError):
                controller.delivery_records(root, {}, 'owned')

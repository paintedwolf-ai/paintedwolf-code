"""The browser sandbox case runs the real page tool and judges only exact live descendants."""
import json
from pathlib import Path
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import browser

ENGINE = {'pid': 10, 'parent_pid': 5, 'start_time_ticks': 30}
HOST = {'user': 1, 'mnt': 2, 'pid': 3, 'net': 4}
IDENTITIES = [{'pid': 12, 'start_ticks': '40'}]
BINDING = {'images': {}}


def page_row() -> dict:
    return {'tool': 'page_open', 'outcome': 'completed', 'invocation': {
        'tool': 'page_open', 'invoked': True, 'status': 'completed'},
        'content': json.dumps({'id': 'page_host_123', 'live_pages': ['page_host_123']})}


class Fixture:
    """Patch every installed collaborator and record what the case handed each one."""

    def __init__(self, results=None, states=None, turn_error=None):
        self.written = {}
        self.app = MagicMock()
        self.app.observations = {'engine_identity': ENGINE, 'capture_complete': True}
        self.app.streams.return_value = {'stdout': 'captured'}
        self.turn = MagicMock()
        self.turn.session_id = 'session-1'
        self.turn.observations = {'turn': 'facts'}
        self.turn.wait.return_value = [page_row()] if results is None else results
        if turn_error is not None:
            self.turn.submit.side_effect = turn_error
        self.states = states if states is not None else ['Z']

    def tool_turn(self, app, provider, project):
        self.written['fixture.html'] = (Path(project) / 'fixture.html').read_text()
        self.project = project
        manager = MagicMock()
        manager.__enter__.return_value = self.turn
        return manager

    def __enter__(self):
        app_manager = MagicMock()
        app_manager.__enter__.return_value = self.app
        self.patches = [
            patch.object(browser, 'ProviderFixture', MagicMock()),
            patch.object(browser, 'InstalledApp', MagicMock(return_value=app_manager)),
            patch.object(browser, 'ToolTurn', side_effect=self.tool_turn),
            patch.object(browser, 'wait_settled'),
            patch.object(browser, 'namespace_observer', return_value=HOST),
            patch.object(browser, 'observe', return_value={'engine': ENGINE, 'browsers': []}),
            patch.object(browser, 'validate', return_value=IDENTITIES),
            patch.object(browser, 'state', side_effect=self.states),
            patch.object(browser, 'cleanup'),
        ]
        self.mocks = [item.start() for item in self.patches]
        return self

    def __exit__(self, *_):
        for item in self.patches:
            item.stop()

    def mock(self, name: str) -> MagicMock:
        return self.mocks[['ProviderFixture', 'InstalledApp', 'ToolTurn', 'wait_settled', 'namespace_observer',
                           'observe', 'validate', 'state', 'cleanup'].index(name)]


class BrowserSandboxTests(unittest.TestCase):
    def test_passes_only_when_every_exact_descendant_is_gone(self):
        with Fixture() as fixture:
            verdict, facts, streams = browser.browser_sandbox(BINDING)
        self.assertEqual(verdict, 'passed')
        self.assertEqual(streams, {'stdout': 'captured'})
        self.assertEqual(fixture.written['fixture.html'], browser.HTML)
        self.assertFalse(Path(fixture.project).exists())
        self.assertEqual(fixture.mock('ProviderFixture').call_args.args[0],
                         [{'name': 'page_open', 'arguments': {'path': 'fixture.html'}}])
        self.assertEqual(fixture.mock('InstalledApp').call_args.args[0], BINDING)
        fixture.turn.submit.assert_called_once_with()
        fixture.turn.wait.assert_called_once_with(timeout=90)
        fixture.mock('wait_settled').assert_called_once_with(fixture.app, 'session-1')
        fixture.mock('observe').assert_called_once_with(BINDING, ENGINE)
        self.assertEqual(fixture.mock('validate').call_args.args[:3], ({'engine': ENGINE, 'browsers': []}, ENGINE, HOST))
        fixture.mock('cleanup').assert_called_once_with(IDENTITIES)
        self.assertEqual(facts['opened']['page']['id'], 'page_host_123')
        self.assertEqual(facts['engine_namespaces'], HOST)
        self.assertEqual(facts['turn'], {'turn': 'facts'})
        self.assertEqual(facts['browser_processes_remaining_after_stop'], [])
        self.assertEqual(len(facts['fixture_sha256']), 64)

    def test_live_descendants_or_incomplete_capture_fail_the_case(self):
        with Fixture(states=['S']) as fixture:
            verdict, facts, _ = browser.browser_sandbox(BINDING)
        self.assertEqual(verdict, 'failed')
        self.assertEqual(facts['browser_processes_remaining_after_stop'], IDENTITIES)
        fixture.mock('cleanup').assert_called_once_with(IDENTITIES)
        with Fixture() as fixture:
            fixture.app.observations['output_limit'] = True
            verdict, _, _ = browser.browser_sandbox(BINDING)
        self.assertEqual(verdict, 'failed')
        with Fixture() as fixture:
            del fixture.app.observations['capture_complete']
            verdict, _, _ = browser.browser_sandbox(BINDING)
        self.assertEqual(verdict, 'failed')

    def test_prose_results_and_turn_failures_still_clean_up_exact_descendants(self):
        with Fixture(results=[dict(page_row(), content='the page opened fine')]) as fixture:
            with self.assertRaises(ValueError):
                browser.browser_sandbox(BINDING)
        fixture.mock('cleanup').assert_called_once_with([])
        fixture.mock('validate').assert_not_called()
        with Fixture(turn_error=RuntimeError('engine went away')) as fixture:
            with self.assertRaises(RuntimeError):
                browser.browser_sandbox(BINDING)
        fixture.mock('cleanup').assert_called_once_with([])


if __name__ == '__main__':
    unittest.main()

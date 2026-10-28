"""The cancellation scenario stops one real daemon build and proves its builder and output are gone."""
import json
from pathlib import Path
import shlex
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import nix_cancellation
from linux_release.installed_cases.records import CaseUnavailable

BINDING = {'binding': True}
SESSION = '11111111-1111-4111-8111-111111111111'
CLIENT = '/nix/store/' + 'a' * 32 + '-nix/bin/nix'
DRV = '/nix/store/' + 'd' * 32 + '-cancel.drv'
OUTPUT = '/nix/store/' + 'e' * 32 + '-cancel'
OWNED = {'owned': [{'pid': 42, 'start_ticks': '99'}], 'live': [{'pid': 42}]}
GONE = {'owned': [], 'live': []}


def fixture() -> dict:
    return {'client': {'path': CLIENT}, 'static_builder': {'path': '/nix/store/busybox'},
            'cancellation': {'derivation': DRV, 'output': OUTPUT, 'token': 'c' * 32}}


class Scenario:
    """Patch every installed and Nix collaborator; the owned-builder and mirror answers are scripted."""

    def __init__(self, *, authority='passed', realized=(False, False), observe=None, stop=None,
                 mirrors=None, fixtures=None, ping='{"version": "2.24.0"}'):
        self.authority = authority
        self.realized_answers = list(realized)
        self.observe_answers = list(observe if observe is not None else [OWNED, GONE, GONE])
        self.observe_calls = []
        self.stop = {'process_id': 'command_7', 'stop_requested': True} if stop is None else stop
        self.mirrors = list(mirrors if mirrors is not None else [{'running': False, 'exit_code': 143}])
        self.fixtures = list(fixtures if fixtures is not None else [fixture(), fixture()])
        self.ping = ping
        self.app = MagicMock()
        self.app.observations = {'capture_complete': True}
        self.app.streams.return_value = {'stdout': b'engine'}
        self.turn = MagicMock()
        self.turn.session_id = SESSION
        self.turn.observations = {'turn': 'facts'}
        self.turn.wait.return_value = [{'tool': 'command'}]
        self.provider = MagicMock()
        self.provider.observations = {'provider': 'facts'}
        self.posts = []

    def observe(self, record, operation):
        self.observe_calls.append(operation)
        if operation == 'cleanup':
            return {'cleaned': True}
        return self.observe_answers.pop(0) if self.observe_answers else GONE

    def accepted(self, app, method, path, payload, statuses):
        self.posts.append((method, path, payload))
        return self.stop

    def __enter__(self):
        manager = lambda value: MagicMock(**{'__enter__.return_value': value})
        self.patches = [
            patch('linux_release.installed_cases.nix_workflows.exercise',
                  return_value=(self.authority, {'ping': True}, {'stderr': b''})),
            patch.object(nix_cancellation, 'load', side_effect=lambda binding: self.fixtures.pop(0)),
            patch.object(nix_cancellation, 'realized', side_effect=lambda program, output: self.realized_answers.pop(0)),
            patch.object(nix_cancellation, 'observe', side_effect=self.observe),
            patch.object(nix_cancellation, 'InstalledApp', MagicMock(return_value=manager(self.app))),
            patch.object(nix_cancellation, 'ProviderFixture', MagicMock(return_value=manager(self.provider))),
            patch.object(nix_cancellation, 'ToolTurn', MagicMock(return_value=manager(self.turn))),
            patch.object(nix_cancellation, 'approve_host_execution'),
            patch.object(nix_cancellation, 'handle', return_value='command_7'),
            patch.object(nix_cancellation, 'mirror', side_effect=lambda app, session, process: self.mirrors.pop(0) if len(self.mirrors) > 1 else self.mirrors[0]),
            patch.object(nix_cancellation, 'accepted', side_effect=self.accepted),
            patch.object(nix_cancellation, 'nix_command', return_value=self.ping),
            patch.object(nix_cancellation.time, 'sleep'),
        ]
        self.mocks = [item.start() for item in self.patches]
        return self

    def __exit__(self, *_):
        for item in self.patches:
            item.stop()

    def arguments(self) -> dict:
        return self.mocks[5].call_args.args[0][0]['arguments']


class CancellationScenarioTests(unittest.TestCase):
    def test_a_stopped_build_with_a_dead_builder_and_no_output_passes(self):
        with Scenario() as scenario:
            status, facts, streams = nix_cancellation.scenario(BINDING)
        self.assertEqual(status, 'passed')
        self.assertEqual(streams, {'stdout': b'engine'})
        self.assertEqual(facts['before'], OWNED)
        self.assertEqual(facts['after'], GONE)
        self.assertEqual(facts['client_process'], {'running': False, 'exit_code': 143})
        self.assertTrue(facts['incomplete_output_refused'])
        self.assertEqual(facts['daemon_after_cancel'], {'version': '2.24.0'})
        self.assertFalse(facts['cleanup_required'])
        self.assertEqual((facts['turn'], facts['provider'], facts['app']),
                         ({'turn': 'facts'}, {'provider': 'facts'}, {'capture_complete': True}))
        arguments = scenario.arguments()
        self.assertTrue(arguments['background'])
        self.assertEqual(arguments['capability_request'], {'host_execution': True})
        command = shlex.split(arguments['command'])
        self.assertEqual(command[0], CLIENT)
        self.assertIn('--offline', command)
        self.assertEqual(command[-1], DRV + '^out')
        self.assertEqual(scenario.posts, [('POST', f'/v1/sessions/{SESSION}/background/command_7/stop', {})])
        scenario.mocks[7].assert_called_once_with(scenario.turn, arguments)
        self.assertEqual(scenario.observe_calls, ['observe', 'state', 'state'])
        self.assertEqual(scenario.mocks[11].call_args.args, (Path(CLIENT), 'store', 'ping', '--json'))

    def test_failed_daemon_authority_fails_before_any_build(self):
        with Scenario(authority='failed') as scenario:
            status, facts, streams = nix_cancellation.scenario(BINDING)
        self.assertEqual(status, 'failed')
        self.assertEqual(facts, {'daemon_authority': {'ping': True}})
        self.assertEqual(streams, {'stderr': b''})
        scenario.mocks[4].assert_not_called()

    def test_an_already_realized_derivative_cannot_prove_a_fresh_cancellation(self):
        with Scenario(realized=(True,)) as scenario, self.assertRaises(CaseUnavailable):
            nix_cancellation.scenario(BINDING)
        scenario.mocks[4].assert_not_called()

    def test_a_builder_that_never_starts_is_unavailable_and_needs_no_cleanup(self):
        clock = iter([0.0, 100.0])
        with Scenario(observe=[GONE]) as scenario, patch.object(nix_cancellation.time, 'monotonic', lambda: next(clock, 200.0)):
            with self.assertRaises(CaseUnavailable):
                nix_cancellation.scenario(BINDING)
        self.assertEqual(scenario.posts, [])
        self.assertNotIn('cleanup', scenario.observe_calls)

    def test_a_stop_that_selects_another_process_is_refused_and_the_builder_is_cleaned_up(self):
        with Scenario(stop={'process_id': 'command_8', 'stop_requested': True},
                      observe=[OWNED, OWNED]) as scenario:
            with self.assertRaises(ValueError):
                nix_cancellation.scenario(BINDING)
        self.assertEqual(scenario.observe_calls[-2:], ['state', 'cleanup'])

    def test_a_surviving_builder_or_a_realized_output_fails(self):
        clock = iter(range(0, 1000, 10))
        with Scenario(observe=[OWNED, OWNED, OWNED]) as scenario, \
                patch.object(nix_cancellation.time, 'monotonic', lambda: float(next(clock))):
            status, facts, _ = nix_cancellation.scenario(BINDING)
        self.assertEqual(status, 'failed')
        self.assertTrue(facts['cleanup_required'])
        self.assertEqual(facts['cleanup'], {'cleaned': True})
        with Scenario(realized=(False, True)):
            status, facts, _ = nix_cancellation.scenario(BINDING)
        self.assertEqual(status, 'failed')
        self.assertFalse(facts['incomplete_output_refused'])

    def test_a_daemon_that_stops_answering_or_a_changed_fixture_is_not_a_pass(self):
        with Scenario(ping='{"status": "gone"}'):
            status, facts, _ = nix_cancellation.scenario(BINDING)
        self.assertEqual(status, 'failed')
        self.assertEqual(facts['daemon_after_cancel'], {'status': 'gone'})
        changed = fixture()
        changed['client']['path'] = CLIENT + '-other'
        with Scenario(fixtures=[fixture(), changed]), self.assertRaises(ValueError):
            nix_cancellation.scenario(BINDING)


if __name__ == '__main__':
    unittest.main()

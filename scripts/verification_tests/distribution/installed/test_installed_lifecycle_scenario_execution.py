"""The lifecycle scenario binds cancellation and release to exact owned process identities."""
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import lifecycle

BINDING = {'binding': True}
SESSIONS = ['11111111-1111-4111-8111-111111111111', '22222222-2222-4222-8222-222222222222']
SINGLE = [{'inner_pid': 11, 'start_ticks': '5'}]
DETACHED = [{'inner_pid': 12, 'start_ticks': '6'}, {'inner_pid': 13, 'start_ticks': '7'}]


def members(rows: list[dict]) -> list[dict]:
    return [{'pid': 1000 + row['inner_pid'], 'start_ticks': row['start_ticks'], 'namespace': 7,
             'inner_pid': row['inner_pid'], 'uids': [1000] * 4} for row in rows]


def result(handle: str) -> list[dict]:
    return [{'tool': 'command', 'process': {'handle': handle, 'running': True},
             'invocation': {'tool': 'command', 'invoked': True}}]


class Scenario:
    """Two scripted turns: the auxiliary single process and the detached target."""

    def __init__(self, *, exit_code=143, auxiliary_running=True, target_alive=False, stop=None,
                 captured=None, first_child_reaped=True):
        self.exit_code, self.auxiliary_running, self.target_alive = exit_code, auxiliary_running, target_alive
        self.stop = stop
        self.captured = captured
        self.first_child_reaped = first_child_reaped
        self.app = MagicMock()
        self.app.observations = {'capture_complete': True}
        self.app.streams.return_value = {'stdout': b'engine'}
        self.turns, self.requests, self.workspaces, self.projects = [], [], [], []

    def tool_turn(self, app, provider, project):
        index = len(self.turns)
        self.projects.append(Path(project))
        self.scripts = getattr(self, 'scripts', []) + [(Path(project) / 'lifecycle.py').read_text()]
        turn = MagicMock()
        turn.session_id = SESSIONS[index]
        turn.observations = {'turn': index}
        turn.wait.return_value = result('command_%d' % index)
        workspace = tempfile.TemporaryDirectory()
        self.workspaces.append(workspace)
        turn.workspace = Path(workspace.name)
        rows = SINGLE if index == 0 else DETACHED
        (turn.workspace / 'qualification-observation.json').write_text(json.dumps(
            {'namespace': 7, 'members': rows, 'first_child_reaped': self.first_child_reaped}))
        self.turns.append(turn)
        manager = MagicMock()
        manager.__enter__.return_value = turn
        return manager

    def capture(self, witness, host_execution=False):
        self.captures = getattr(self, 'captures', []) + [host_execution]
        if self.captured is not None:
            return self.captured
        return members(witness['members'])

    def accepted(self, app, method, path, payload, statuses):
        self.requests.append((method, path, payload))
        if method == 'POST':
            return self.stop if self.stop is not None else {'process_id': path.split('/')[-2], 'stop_requested': True}
        if SESSIONS[0] in path:
            return {'processes': [{'process_id': 'command_0', 'running': self.auxiliary_running}]}
        return {'processes': [{'process_id': 'command_1', 'running': False, 'exit_code': self.exit_code}]}

    def state(self, row):
        auxiliary = row['pid'] in {1000 + member['inner_pid'] for member in SINGLE}
        return 'S' if auxiliary or self.target_alive else None

    def __enter__(self):
        app_manager = MagicMock()
        app_manager.__enter__.return_value = self.app
        provider = MagicMock()
        provider.observations = {'provider': 'facts'}
        provider_manager = MagicMock()
        provider_manager.__enter__.return_value = provider
        self.patches = [patch.object(lifecycle, 'InstalledApp', MagicMock(return_value=app_manager)),
                        patch.object(lifecycle, 'ProviderFixture', MagicMock(return_value=provider_manager)),
                        patch.object(lifecycle, 'ToolTurn', side_effect=self.tool_turn),
                        patch.object(lifecycle, 'approve_host_execution'),
                        patch.object(lifecycle, 'capture', side_effect=self.capture),
                        patch.object(lifecycle, 'accepted', side_effect=self.accepted),
                        patch.object(lifecycle, 'state', side_effect=self.state),
                        patch.object(lifecycle, 'cleanup'),
                        patch.object(lifecycle.time, 'sleep')]
        self.mocks = [item.start() for item in self.patches]
        return self

    def __exit__(self, *_):
        for item in self.patches:
            item.stop()
        for workspace in self.workspaces:
            workspace.cleanup()

    def arguments(self, index: int) -> dict:
        return self.mocks[1].call_args_list[index].args[0][0]['arguments']


class LifecycleScenarioTests(unittest.TestCase):
    def test_cancelling_the_detached_target_leaves_the_unrelated_process_running(self):
        with Scenario() as scenario:
            status, facts, streams = lifecycle.scenario(BINDING, True)
        self.assertEqual(status, 'passed')
        self.assertEqual(streams, {'stdout': b'engine'})
        self.assertEqual(facts['stop'], {'process_id': 'command_1', 'stop_requested': True})
        self.assertTrue(facts['unrelated_survives'])
        self.assertEqual(facts['target_processes'], members(DETACHED))
        self.assertEqual(facts['unrelated_processes'], members(SINGLE))
        self.assertEqual(facts['terminal_process'], {'process_id': 'command_1', 'running': False, 'exit_code': 143})
        self.assertEqual(facts['target'], {'turn': 1})
        self.assertEqual(scenario.requests[0], ('POST', f'/v1/sessions/{SESSIONS[1]}/background/command_1/stop', {}))
        for index, mode in enumerate(('single', 'detached')):
            arguments = scenario.arguments(index)
            self.assertTrue(arguments['background'])
            self.assertTrue(arguments['allow_concurrent'])
            self.assertEqual(arguments['command'].split()[-1], mode)
            self.assertEqual(scenario.scripts[index], lifecycle.SCRIPT)
            self.assertNotIn('capability_request', arguments)
        self.assertEqual(scenario.captures, [False, False])
        scenario.mocks[3].assert_not_called()
        scenario.mocks[7].assert_called_once_with(members(SINGLE) + members(DETACHED))

    def test_releasing_the_target_expects_a_clean_foreground_exit(self):
        with Scenario(exit_code=0) as scenario:
            status, facts, _ = lifecycle.scenario(BINDING, False)
            released = [(turn.workspace / 'qualification-release').exists() for turn in scenario.turns]
            release = (scenario.turns[1].workspace / 'qualification-release').read_text()
        self.assertEqual(status, 'passed')
        self.assertIsNone(facts['stop'])
        self.assertEqual([request[0] for request in scenario.requests], ['GET', 'GET'])
        self.assertEqual((released, release), ([False, True], 'release fixed fixture\n'))
        self.assertTrue(scenario.arguments(0)['background'])
        self.assertFalse(scenario.arguments(1)['background'])
        with Scenario(exit_code=143):
            status, _, _ = lifecycle.scenario(BINDING, False)
        self.assertEqual(status, 'failed')

    def test_host_execution_is_requested_and_approved_only_for_the_target(self):
        with Scenario() as scenario:
            status, _, _ = lifecycle.scenario(BINDING, True, host_execution=True)
        self.assertEqual(status, 'passed')
        self.assertNotIn('capability_request', scenario.arguments(0))
        self.assertEqual(scenario.arguments(1)['capability_request'], {'host_execution': True})
        scenario.mocks[3].assert_called_once_with(scenario.turns[1], scenario.arguments(1))
        self.assertEqual(scenario.captures, [False, True])

    def test_a_dead_unrelated_process_a_live_target_or_an_unreaped_child_fails(self):
        refusals = [dict(auxiliary_running=False), dict(target_alive=True), dict(first_child_reaped=False), dict(exit_code=0)]
        for changed in refusals:
            clock = iter(range(0, 1000, 5))
            with self.subTest(**changed), Scenario(**changed) as scenario, \
                    patch.object(lifecycle.time, 'monotonic', lambda: float(next(clock))):
                status, facts, _ = lifecycle.scenario(BINDING, True)
            self.assertEqual(status, 'failed')
            self.assertEqual(facts['unrelated_survives'], changed.get('auxiliary_running', True))
            scenario.mocks[7].assert_called_once()

    def test_a_stop_for_another_process_or_a_lost_member_is_refused_with_cleanup(self):
        with Scenario(stop={'process_id': 'command_0', 'stop_requested': True}) as scenario, self.assertRaises(ValueError):
            lifecycle.scenario(BINDING, True)
        scenario.mocks[7].assert_called_once_with(members(SINGLE) + members(DETACHED))
        with Scenario(captured=members(SINGLE)) as scenario, self.assertRaises(ValueError):
            lifecycle.scenario(BINDING, True)
        scenario.mocks[7].assert_called_once_with(members(SINGLE) * 2)

    def test_background_cancel_is_the_cancelling_scenario(self):
        with patch.object(lifecycle, 'scenario', return_value=('passed', {}, {})) as scenario:
            self.assertEqual(lifecycle.background_cancel(BINDING), ('passed', {}, {}))
        scenario.assert_called_once_with(BINDING, True)


class StateAndWitnessTests(unittest.TestCase):
    def test_state_binds_the_stat_record_to_the_captured_start_ticks(self):
        row = '42 (engine with ) spaces) ' + ' '.join(['S'] + ['0'] * 18 + ['77', '0'])
        identity = {'pid': 42, 'start_ticks': '77'}
        with patch.object(Path, 'read_text', return_value=row):
            self.assertEqual(lifecycle.state(identity), 'S')
            self.assertIsNone(lifecycle.state(dict(identity, start_ticks='78')))
        with patch.object(Path, 'read_text', side_effect=FileNotFoundError()):
            self.assertIsNone(lifecycle.state(identity))
        with patch.object(Path, 'read_text', return_value='42 (short) S 0 0'), self.assertRaises(ValueError):
            lifecycle.state(identity)

    def test_witness_is_read_from_the_workspace_within_its_bound(self):
        with tempfile.TemporaryDirectory() as temporary:
            workspace = Path(temporary)
            turn = SimpleNamespace(workspace=workspace)
            path = workspace / 'qualification-observation.json'
            path.write_text(json.dumps({'namespace': 7, 'members': SINGLE}))
            self.assertEqual(lifecycle.witness(turn), {'namespace': 7, 'members': SINGLE})
            path.write_text('x' * 4097)
            with self.assertRaises(ValueError):
                lifecycle.witness(turn)
            path.unlink()
            (workspace / 'elsewhere.json').write_text('{}')
            path.symlink_to(workspace / 'elsewhere.json')
            with self.assertRaises(ValueError):
                lifecycle.witness(turn)
            path.unlink()
            clock = iter([0.0, 100.0])
            with patch.object(lifecycle.time, 'monotonic', lambda: next(clock, 200.0)), self.assertRaises(ValueError):
                lifecycle.witness(turn)

    def test_capture_refuses_malformed_members_and_unbound_observer_rows(self):
        native = lifecycle.Path

        class NamespacePath:
            def stat(self):
                return SimpleNamespace(st_ino=42)

        def path(value):
            return NamespacePath() if value == '/proc/self/ns/pid' else native(value)
        witness = {'namespace': 42, 'members': [{'inner_pid': 123, 'start_ticks': '7'}]}
        with patch.object(lifecycle, 'Path', side_effect=path), patch.object(lifecycle.os, 'getuid', return_value=1000):
            for malformed in [[{'inner_pid': 0, 'start_ticks': '7'}], [{'inner_pid': 123, 'start_ticks': 7}],
                              [{'inner_pid': 123}], 'members', []]:
                with self.subTest(members=malformed), self.assertRaises(ValueError):
                    lifecycle.capture(dict(witness, members=malformed), True)
            with patch.object(lifecycle.subprocess, 'run', return_value=SimpleNamespace(returncode=1, stdout=b'')):
                with self.assertRaises(OSError):
                    lifecycle.capture(witness, True)
            foreign = {'pid': 999, 'inner_pid': 124, 'start_ticks': '7', 'namespace': 42, 'uids': [1000] * 4}
            root = {'pid': 999, 'inner_pid': 123, 'start_ticks': '7', 'namespace': 42, 'uids': [0] * 4}
            for rows in [[foreign], [root]]:
                with self.subTest(rows=rows), patch.object(lifecycle.subprocess, 'run', return_value=SimpleNamespace(
                        returncode=0, stdout=json.dumps(rows).encode())), self.assertRaises(ValueError):
                    lifecycle.capture(witness, True)
            with patch.object(lifecycle.subprocess, 'run', return_value=SimpleNamespace(returncode=0, stdout=b'[]')):
                self.assertEqual(lifecycle.capture(witness, True), [])


if __name__ == '__main__':
    unittest.main()

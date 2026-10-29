"""Killing the captured engine must take only the command's own processes with it."""
import json
from pathlib import Path
import subprocess
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import crash

ENGINE = {'pid': 10, 'parent_pid': 5, 'start_time_ticks': 30}
OUTSIDE = {'pid': 4321, 'parent_pid': 1, 'start_time_ticks': 77}
MEMBERS = [{'pid': 21, 'start_ticks': '41'}, {'pid': 22, 'start_ticks': '42'}]
BINDING = {'binding': True}


class Clock:
    def __init__(self):
        self.now = 0.0

    def monotonic(self):
        self.now += 6.0
        return self.now

    def sleep(self, _):
        return


class Unrelated:
    pid = 4321

    def __init__(self, arguments, **options):
        self.arguments, self.options, self.waited = arguments, options, None

    def poll(self):
        return None

    def wait(self, timeout=None):
        self.waited = timeout
        return 0


class Crash:
    """Patch the installed collaborators of the engine crash scenario."""

    def __init__(self, members=MEMBERS, states=None, reaped=True, outside_after=OUTSIDE):
        self.members, self.reaped, self.outside_after = members, reaped, outside_after
        self.states = {} if states is None else states
        self.app = MagicMock()
        self.app.observations = {'engine_identity': ENGINE}
        self.app.streams.return_value = {'stdout': 'engine'}
        self.turn = MagicMock()
        self.turn.observations = {'turn': 'facts'}
        self.turn.wait.return_value = {'raw': 'handle'}
        self.identities = iter([OUTSIDE, outside_after])
        self.written = {}
        self.spawned = []

    def popen(self, arguments, **options):
        process = Unrelated(arguments, **options)
        self.spawned.append(process)
        return process

    def tool_turn(self, app, provider, root):
        self.written = {'lifecycle.py': (Path(root) / 'lifecycle.py').read_text()}
        manager = MagicMock()
        manager.__enter__.return_value = self.turn
        return manager

    def __enter__(self):
        app_manager = MagicMock()
        app_manager.__enter__.return_value = self.app
        self.patches = [
            patch.object(crash, 'InstalledApp', MagicMock(return_value=app_manager)),
            patch.object(crash, 'subprocess', SimpleNamespace(Popen=self.popen, DEVNULL=subprocess.DEVNULL)),
            patch.object(crash, 'process_identity', side_effect=lambda pid: next(self.identities)),
            patch.object(crash, 'ProviderFixture', MagicMock()),
            patch.object(crash, 'ToolTurn', side_effect=self.tool_turn),
            patch.object(crash, 'approve_host_execution'),
            patch.object(crash, 'handle', return_value={'pid': 21}),
            patch.object(crash, 'witness', return_value={'first_child_reaped': self.reaped}),
            patch.object(crash, 'capture', return_value=list(self.members)),
            patch.object(crash, 'kill_engine', return_value={'identity': ENGINE, 'signal': 'SIGKILL', 'exit_observed': True}),
            patch.object(crash, 'state', side_effect=lambda row: self.states.get(row['pid'])),
            patch.object(crash, 'time', Clock()),
            patch.object(crash, 'cleanup'),
        ]
        self.mocks = [item.start() for item in self.patches]
        return self

    def __exit__(self, *_):
        for item in self.patches:
            item.stop()

    def mock(self, name):
        names = ['InstalledApp', 'subprocess', 'process_identity', 'ProviderFixture', 'ToolTurn', 'approve_host_execution',
                 'handle', 'witness', 'capture', 'kill_engine', 'state', 'time', 'cleanup']
        return self.mocks[names.index(name)]


class EngineCrashTests(unittest.TestCase):
    def test_command_processes_vanish_and_the_unrelated_process_survives(self):
        with Crash() as scenario:
            status, facts, streams = crash.engine_crash(BINDING, False)
        self.assertEqual(status, 'passed')
        self.assertEqual(streams, {'stdout': 'engine'})
        self.assertEqual(scenario.written['lifecycle.py'], crash.SCRIPT)
        unrelated = scenario.spawned[0]
        self.assertEqual(unrelated.arguments[1:], ['-c', 'import time; time.sleep(120)'])
        self.assertTrue(unrelated.options['start_new_session'])
        self.assertEqual(unrelated.waited, 5)
        provider = scenario.mock('ProviderFixture').call_args.args[0][0]
        self.assertEqual(provider['name'], 'command')
        self.assertEqual((provider['arguments']['background'], provider['arguments']['wait_ms'], provider['arguments']['timeout_ms']), (True, 1, 90000))
        self.assertNotIn('capability_request', provider['arguments'])
        self.assertTrue(provider['arguments']['command'].endswith('lifecycle.py detached'))
        scenario.mock('approve_host_execution').assert_not_called()
        scenario.mock('kill_engine').assert_called_once_with(ENGINE)
        self.assertIsNone(scenario.turn.session_id)
        scenario.mock('cleanup').assert_called_once_with([{'pid': 4321, 'start_ticks': '77'}, *MEMBERS])
        self.assertEqual(facts['engine_crash']['signal'], 'SIGKILL')
        self.assertEqual((facts['host_execution'], facts['command_processes'], facts['command_processes_gone'],
                          facts['unrelated_identity'], facts['unrelated_survives'], facts['terminal_api_receipt_available']),
                         (False, MEMBERS, True, OUTSIDE, True, False))
        self.assertEqual(facts['turn'], {'turn': 'facts'})

    def test_host_execution_requests_the_capability_and_approves_it(self):
        with Crash() as scenario:
            status, facts, _ = crash.engine_crash(BINDING, True)
        self.assertEqual(status, 'passed')
        arguments = scenario.mock('ProviderFixture').call_args.args[0][0]['arguments']
        self.assertEqual(arguments['capability_request'], {'host_execution': True})
        scenario.mock('approve_host_execution').assert_called_once_with(scenario.turn, arguments)
        scenario.mock('capture').assert_called_once_with({'first_child_reaped': True}, True)
        self.assertTrue(facts['host_execution'])

    def test_surviving_command_processes_or_a_lost_bystander_fail(self):
        with Crash(states={22: 'S'}) as scenario:
            status, facts, _ = crash.engine_crash(BINDING, False)
        self.assertEqual(status, 'failed')
        self.assertFalse(facts['command_processes_gone'])
        scenario.mock('cleanup').assert_called_once()
        with Crash(outside_after=dict(OUTSIDE, start_time_ticks=78)):
            status, facts, _ = crash.engine_crash(BINDING, False)
        self.assertEqual(status, 'failed')
        self.assertFalse(facts['unrelated_survives'])

    def test_command_must_be_live_before_the_deliberate_crash(self):
        for scenario in (Crash(members=MEMBERS[:1]), Crash(reaped=False)):
            with scenario, self.assertRaisesRegex(ValueError, 'not live before'):
                crash.engine_crash(BINDING, False)
            scenario.mock('kill_engine').assert_not_called()
            scenario.mock('cleanup').assert_called_once()
            self.assertEqual(scenario.mock('cleanup').call_args.args[0][0], {'pid': 4321, 'start_ticks': '77'})
            self.assertEqual(scenario.spawned[0].waited, 5)


if __name__ == '__main__':
    unittest.main()

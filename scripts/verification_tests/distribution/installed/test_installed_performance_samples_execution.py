"""Installed performance samples come from real app lifecycles with bounded evidence."""
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import performance

LIMITS = {'startup.ready': 7500, 'restart.ready': 7500, 'project.create': 1000, 'session.prepare': 1000,
          'prompt.accept': 1000, 'prompt.settle': 5000, 'peak_rss_bytes': 512 << 20, 'rss_growth_bytes': 16 << 20}
IDENTITY = {'pid': 42, 'parent_pid': 7, 'start_time_ticks': 9}


class Clock:
    def __init__(self, step):
        self.now, self.step = 0.0, step

    def __call__(self):
        self.now += self.step
        return self.now


def installed_app(complete=True, limit=False, streams=None):
    app = MagicMock()
    app.observations = {'engine_identity': IDENTITY, 'capture_complete': complete}
    if limit:
        app.observations['output_limit'] = True
    app.streams.return_value = {'app-stdout': b'out', 'app-stderr': b'err'} if streams is None else streams
    app.__enter__.return_value = app
    return app


class StartSampleTests(unittest.TestCase):
    def measure(self, app, limits=LIMITS, step=0.5):
        with patch.object(performance, 'limits', return_value=limits), \
                patch.object(performance, 'InstalledApp', return_value=app) as launches, \
                patch('linux_release.installed_cases.desktop_ui.interactive_window', return_value={'window': 'main'}) as window, \
                patch.object(performance.time, 'monotonic', Clock(step)):
            facts, streams = performance.start_samples({'artifact': {}})
        return facts, streams, launches, window

    def test_twenty_launches_measure_restart_ready_against_the_absolute_limit(self):
        app = installed_app()
        facts, streams, launches, window = self.measure(app)
        self.assertEqual(launches.call_count, 20)
        self.assertEqual(launches.call_args.kwargs, {'timeout': 60})
        self.assertEqual(window.call_count, 20)
        self.assertEqual(app.stop.call_count, 20)
        self.assertEqual(facts['measurements_ms']['restart.ready']['samples'], 20)
        self.assertEqual(facts['samples_ms'], [500.0] * 20)
        self.assertEqual(facts['failures'], [])
        self.assertEqual(facts['absolute_limits'], LIMITS)
        self.assertEqual(facts['windows'], [{'window': 'main'}] * 20)
        self.assertEqual(facts['engine_identities'], [IDENTITY] * 20)
        self.assertEqual(facts['cache_state'], 'uncontrolled')
        self.assertIn('boot-bound-cold-launch', facts['remaining_requirements'])
        self.assertEqual(streams, {'app-stdout': b'out' * 20, 'app-stderr': b'err' * 20})

    def test_a_restart_slower_than_the_absolute_limit_is_a_failure(self):
        facts, _, _, _ = self.measure(installed_app(), limits=dict(LIMITS, **{'restart.ready': 400}))
        self.assertEqual(facts['failures'], [{'metric': 'restart.ready', 'p95': 500.0, 'absolute_limit': 400}])

    def test_a_launch_without_bounded_capture_or_within_evidence_bounds_is_refused(self):
        for app in [installed_app(complete=False), installed_app(limit=True),
                    installed_app(streams={'app-stdout': b'x' * ((8 << 20) + 1), 'app-stderr': b''})]:
            with self.subTest(observations=app.observations), self.assertRaises(ValueError):
                self.measure(app)
            self.assertEqual(app.stop.call_count, 1)


class WorkflowSampleTests(unittest.TestCase):
    def measure(self, invoked=True, app=None):
        app = installed_app() if app is None else app
        turns = []

        def tool_turn(app_, provider, project):
            turn = MagicMock()
            turn.session_id = 'session'
            turn.observations = {'turn': len(turns), 'project': project.name}
            turn.wait.return_value = [{'tool': 'command'}]
            turn.__enter__.return_value = turn
            turns.append(turn)
            return turn
        provider = MagicMock()
        provider.__enter__.return_value = provider
        with patch.object(performance, 'limits', return_value=LIMITS), \
                patch.object(performance, 'InstalledApp', return_value=app), \
                patch.object(performance, 'ProviderFixture', return_value=provider) as fixtures, \
                patch.object(performance, 'ToolTurn', side_effect=tool_turn), \
                patch.object(performance, 'wait_settled') as settled, \
                patch.object(performance, 'successful_invocation', return_value=invoked) as invocation, \
                patch.object(performance.time, 'monotonic', Clock(0.25)):
            facts, streams = performance.workflow_samples({'artifact': {}})
        return facts, streams, turns, fixtures, settled, invocation, app

    def test_twenty_tool_turns_measure_session_preparation_and_prompt_settlement(self):
        facts, streams, turns, fixtures, settled, invocation, app = self.measure()
        self.assertEqual(len(turns), 20)
        self.assertEqual(facts['samples_ms'], {'session.prepare': [250.0] * 20, 'prompt.settle': [250.0] * 20})
        self.assertEqual(facts['measurements_ms']['prompt.settle']['samples'], 20)
        self.assertEqual(facts['failures'], [])
        self.assertEqual(facts['workload'], 'deterministic-local-provider-command')
        self.assertEqual([row['project'] for row in facts['witnesses']], [str(index) for index in range(20)])
        self.assertEqual(fixtures.call_args.args[0], [{'name': 'command', 'arguments': {'command': 'printf installed-performance-fixture'}}])
        self.assertEqual(settled.call_count, 20)
        self.assertEqual([call.args[0] for call in invocation.call_args_list], [[{'tool': 'command'}]] * 20)
        for turn in turns:
            turn.submit.assert_called_once_with()
            turn.wait.assert_called_once_with(timeout=60)
        app.stop.assert_called_once_with()
        self.assertEqual(streams, {'app-stdout': b'out', 'app-stderr': b'err'})

    def test_a_turn_whose_command_did_not_actually_run_is_refused(self):
        with self.assertRaises(ValueError):
            self.measure(invoked=False)

    def test_a_workflow_without_bounded_capture_is_refused(self):
        with self.assertRaises(ValueError):
            self.measure(app=installed_app(limit=True))


class ResourceSampleTests(unittest.TestCase):
    @staticmethod
    def rows(rss=lambda index: 100 << 20, count=31, elapsed=1.0):
        return [{'at_monotonic': index * elapsed, 'rss_bytes': rss(index), 'cpu_ticks': 10 * index,
                 'context_switches': 3 * index, 'identity': IDENTITY} for index in range(count)]

    def probe(self, rows, app=None):
        app = installed_app() if app is None else app
        with patch.object(performance, 'limits', return_value=LIMITS), \
                patch.object(performance, 'InstalledApp', return_value=app), \
                patch.object(performance, 'resources', side_effect=rows) as observe, \
                patch.object(performance.time, 'sleep') as sleep, \
                patch.object(performance.os, 'sysconf', return_value=100):
            facts, streams = performance.resource_samples({'artifact': {}})
        return facts, streams, observe, sleep, app

    def test_thirty_one_identity_bound_observations_a_second_apart_describe_the_engine(self):
        facts, streams, observe, sleep, app = self.probe(self.rows())
        self.assertEqual(observe.call_count, 31)
        self.assertEqual({call.args[0]['pid'] for call in observe.call_args_list}, {42})
        self.assertEqual(sleep.call_count, 30)
        self.assertEqual((facts['peak_rss_bytes'], facts['rss_growth_bytes'], facts['elapsed_seconds']), (100 << 20, 0, 30.0))
        self.assertEqual(facts['idle_cpu_percent'], 10.0)
        self.assertEqual(facts['main_thread_context_switches_per_second'], 3.0)
        self.assertEqual(facts['failures'], [])
        self.assertEqual(facts['scope'], 'captured-installed-engine')
        self.assertEqual(len(facts['samples']), 31)
        app.stop.assert_called_once_with()
        self.assertEqual(streams, {'app-stdout': b'out', 'app-stderr': b'err'})

    def test_growth_and_peak_beyond_the_absolute_limits_are_failures(self):
        facts, _, _, _, _ = self.probe(self.rows(rss=lambda index: (100 + index) << 20))
        self.assertEqual(facts['failures'], [{'metric': 'rss_growth_bytes', 'measured': 21 << 20, 'absolute_limit': 16 << 20}])
        facts, _, _, _, _ = self.probe(self.rows(rss=lambda index: 600 << 20))
        self.assertEqual(facts['failures'], [{'metric': 'peak_rss_bytes', 'measured': 600 << 20, 'absolute_limit': 512 << 20}])

    def test_regressing_counters_an_empty_interval_or_lost_capture_are_refused(self):
        rows = self.rows()
        rows[5]['cpu_ticks'] = 0
        with self.assertRaises(ValueError):
            self.probe(rows)
        with self.assertRaises(ValueError):
            self.probe(self.rows(elapsed=0.0))
        with self.assertRaises(ValueError):
            self.probe(self.rows(), app=installed_app(complete=False))


if __name__ == '__main__':
    unittest.main()

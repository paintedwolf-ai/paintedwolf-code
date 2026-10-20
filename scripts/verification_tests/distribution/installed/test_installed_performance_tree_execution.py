"""Process-tree observations preserve ownership, lifetime and budget scope."""
from pathlib import Path
import os
import select
import signal
import subprocess
import sys
import unittest
from unittest.mock import patch

from linux_release.installed_cases import performance, performance_resources as owner
from linux_release.installed_cases.records import CaseUnavailable


def identity(pid, parent=1, start=10):
    return {'pid': pid, 'parent_pid': parent, 'start_time_ticks': start}


def observation(lease, ticks=0, rss=1024):
    return {'identity': lease, 'cpu_ticks': ticks, 'rss_bytes': rss,
            'context_switches': 0, 'at_monotonic': 1}


class InstalledPerformanceTreeTests(unittest.TestCase):
    def test_members_excludes_foreign_and_preexisting_parent_pid_reuse(self):
        root, child = identity(20), identity(21, 20, 11)
        rows = {20: root, 21: child, 22: identity(22, 1, 12), 23: identity(23, 20, 9),
                24: identity(24, 21, 12)}
        with patch.object(Path, 'iterdir', return_value=[Path(str(pid)) for pid in rows]), \
                patch.object(owner, 'process_identity', side_effect=lambda pid: rows[pid]):
            self.assertEqual(owner.members(root), [root, child, rows[24]])

    def test_changed_root_is_refused_instead_of_observing_replacement(self):
        with patch.object(owner, 'process_identity', return_value=identity(20, start=99)):
            with self.assertRaisesRegex(ValueError, 'captured identity'):
                owner.members(identity(20))

    def test_cpu_history_preserves_departed_process_and_new_lifetime(self):
        root, engine = identity(20), identity(21, 20, 11)
        child, reused = identity(22, 20, 12), identity(22, 20, 13)
        sampler = owner.TreeSampler(root, engine)
        states = [([root, engine, child], [10, 20, 30]),
                  ([root, engine, child], [11, 22, 35]),
                  ([root, engine, reused], [12, 23, 4])]
        results = []
        for selected, ticks in states:
            measured = {row['pid']: observation(row, tick) for row, tick in zip(selected, ticks)}
            with patch.object(owner, 'members', return_value=selected), \
                    patch.object(owner, 'resources', side_effect=lambda row: measured[row['pid']]):
                results.append(sampler.snapshot())
        self.assertEqual([row['observed_cpu_ticks'] for row in results], [0, 8, 14])
        self.assertEqual(results[-1]['rss_sum_bytes'], 3072)

    def test_counter_regression_and_unrelated_engine_refused(self):
        root, engine = identity(20), identity(21, 20, 11)
        sampler = owner.TreeSampler(root, engine)
        with patch.object(owner, 'members', return_value=[root, engine]), \
                patch.object(owner, 'resources', side_effect=lambda row: observation(row, 5)):
            sampler.snapshot()
        with patch.object(owner, 'members', return_value=[root, engine]), \
                patch.object(owner, 'resources', side_effect=lambda row: observation(row, 4)):
            with self.assertRaisesRegex(ValueError, 'regressed'):
                sampler.snapshot()
        with patch.object(owner, 'members', return_value=[root]):
            with self.assertRaisesRegex(ValueError, 'outside'):
                sampler.snapshot()

    def test_persistent_churn_is_unavailable_not_zero(self):
        root, engine = identity(20), identity(21, 20, 11)
        with patch.object(owner, 'members', side_effect=[[root, engine], [root]] * 3), \
                patch.object(owner, 'resources', side_effect=lambda row: observation(row)):
            with self.assertRaises(CaseUnavailable):
                owner.TreeSampler(root, engine).snapshot()

    def test_tree_limits_and_wakeups_are_not_inferred_from_engine_budget(self):
        samples = [{'at_monotonic': 10, 'observed_cpu_ticks': 0, 'rss_sum_bytes': 1024},
                   {'at_monotonic': 12, 'observed_cpu_ticks': 10, 'rss_sum_bytes': 2048}]
        with patch.object(owner.os, 'sysconf', return_value=100):
            result = owner.tree_summary(samples)
        self.assertEqual(result['observed_cpu_percent_lower_bound'], 5)
        self.assertEqual(result['sampled_peak_rss_sum_bytes'], 2048)
        self.assertIsNone(result['absolute_limits'])
        self.assertFalse(result['wakeups_measured'])
        self.assertFalse(result['baseline_qualified'])
        with self.assertRaises(CaseUnavailable):
            owner.tree_summary(samples[:1])
        samples[-1]['at_monotonic'] = 10
        with self.assertRaises(ValueError):
            owner.tree_summary(samples)

    def test_missing_tree_capture_preserves_engine_measurement_and_does_not_pass(self):
        class App:
            observations = {'engine_identity': identity(21, 20, 11), 'launch_identity': identity(20),
                            'capture_complete': True}
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def stop(self): pass
            def streams(self): return {}
        samples = [dict(observation(identity(21, 20, 11)), at_monotonic=index) for index in range(31)]
        with patch.object(performance, 'InstalledApp', return_value=App()), \
                patch.object(performance, 'resources', side_effect=samples), \
                patch.object(owner.TreeSampler, 'snapshot', side_effect=CaseUnavailable('capture unavailable')), \
                patch.object(performance.time, 'sleep'):
            result, streams = performance.resource_samples({})
        self.assertEqual(len(result['samples']), 31)
        self.assertIsNone(result['desktop_tree'])
        self.assertEqual(len(result['desktop_tree_incomplete_snapshots']), 31)
        self.assertIn('declared-whole-desktop-resource-budgets', result['remaining_requirements'])
        self.assertEqual(result['failures'], [])

    @unittest.skipUnless(sys.platform == 'linux', 'actual /proc process-tree observation requires Linux')
    def test_actual_owned_descendants_and_separate_control_process(self):
        script = ('import subprocess,sys; p=subprocess.Popen([sys.executable,"-c",'
                  '"import time;print(123,flush=True);time.sleep(60)"],stdout=subprocess.PIPE,text=True);'
                  'assert p.stdout.readline()=="123\\n";print(p.pid,flush=True);'
                  'sys.stdin.read();p.terminate();p.wait();p.stdout.close()')
        root = subprocess.Popen([sys.executable, '-c', script], stdin=subprocess.PIPE,
                                stdout=subprocess.PIPE, text=True, start_new_session=True)
        foreign = subprocess.Popen([sys.executable, '-c', 'import time;time.sleep(60)'], start_new_session=True)
        try:
            self.assertTrue(select.select([root.stdout], [], [], 5)[0], 'owned fixture startup timed out')
            child = int(root.stdout.readline(32))
            desktop, engine = owner.process_identity(root.pid), owner.process_identity(child)
            result = owner.TreeSampler(desktop, engine).snapshot()
            pids = {row['identity']['pid'] for row in result['processes']}
            self.assertIn(root.pid, pids)
            self.assertIn(child, pids)
            self.assertNotIn(foreign.pid, pids)
            self.assertGreater(result['rss_sum_bytes'], 0)
            self.assertIsNone(foreign.poll())
        finally:
            root.stdin.close()
            try:
                root.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(root.pid, signal.SIGKILL)
                root.wait(timeout=5)
            root.stdout.close()
            foreign.terminate()
            foreign.wait(timeout=5)

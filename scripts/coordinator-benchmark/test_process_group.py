import signal
import subprocess
import sys
import time
import unittest
from unittest.mock import Mock, call, patch
from process_group import live_members, signal_group, stop_process


class ProcessGroupTests(unittest.TestCase):
    def test_permission_denial_requires_observed_absence_of_live_members(self):
        for rows, live in [('41 Z\n42 S\n', False), ('42 S\n', False),
                           ('41 Z+\n41 S\n', True), ('41 T\n', True)]:
            with self.subTest(rows=rows), \
                 patch('process_group.os.killpg', side_effect=PermissionError(1, 'denied')), \
                 patch('process_group.subprocess.check_output', return_value=rows):
                if live:
                    with self.assertRaises(PermissionError):
                        signal_group(41, signal.SIGTERM)
                else:
                    self.assertFalse(signal_group(41, signal.SIGTERM))

    def test_failed_or_malformed_process_observation_is_not_clean_shutdown(self):
        failures = [subprocess.CalledProcessError(1, 'ps'), ValueError('invalid table')]
        for failure in failures:
            with self.subTest(failure=failure), \
                 patch('process_group.os.killpg', side_effect=PermissionError(1, 'denied')), \
                 patch('process_group.subprocess.check_output', side_effect=failure):
                with self.assertRaises(type(failure)):
                    signal_group(41, signal.SIGTERM)
        for rows in ['not-a-group Z\n', '41\n', '41 S unexpected\n']:
            with self.subTest(rows=rows), patch('process_group.subprocess.check_output', return_value=rows):
                with self.assertRaises(ValueError):
                    live_members(41)

    def test_refuses_broadcast_group_ids(self):
        for pgid in [-41, -1, 0, 1]:
            with self.subTest(pgid=pgid), patch('process_group.os.killpg') as send:
                with self.assertRaises(ValueError):
                    signal_group(pgid, signal.SIGKILL)
                send.assert_not_called()

    def test_missing_group_needs_no_process_table(self):
        with patch('process_group.os.killpg', side_effect=ProcessLookupError), \
             patch('process_group.live_members') as observe:
            self.assertFalse(signal_group(41, signal.SIGTERM))
            observe.assert_not_called()

    def test_forced_cleanup_waits_for_live_children_to_exit(self):
        process = Mock(pid=41)
        process.wait.return_value = 37
        with patch('process_group.signal_group', return_value=True) as send, \
             patch('process_group.live_members', side_effect=[True, True, False]) as observe, \
             patch('process_group.time.sleep'):
            self.assertEqual(stop_process(process, grace_seconds=0), 37)
        self.assertEqual(send.call_args_list, [call(41, signal.SIGTERM), call(41, signal.SIGKILL)])
        self.assertEqual(observe.call_count, 3)
        process.wait.assert_called_once()

    def test_surviving_children_after_kill_are_not_reported_as_cleaned(self):
        process = Mock(pid=41)
        with patch('process_group.signal_group', return_value=True), \
             patch('process_group.live_members', return_value=True):
            with self.assertRaises(TimeoutError):
                stop_process(process, grace_seconds=0, kill_wait_seconds=0)
        process.wait.assert_not_called()

    def test_unreaped_exited_launcher_keeps_its_actual_exit_status(self):
        process = subprocess.Popen([sys.executable, '-c', 'import os; os._exit(37)'],
                                   start_new_session=True)
        try:
            deadline = time.monotonic() + 15
            while True:
                rows = subprocess.check_output(['ps', '-axo', 'pid=,stat='], text=True)
                if any(int(pid) == process.pid and state[0] == 'Z'
                       for pid, state in (row.split() for row in rows.splitlines())):
                    break
                self.assertLess(time.monotonic(), deadline, 'child did not reach unreaped exit state')
                time.sleep(.02)
            self.assertFalse(live_members(process.pid))
            self.assertEqual(stop_process(process, grace_seconds=.1), 37)
            self.assertFalse(live_members(process.pid))
        finally:
            if process.poll() is None:
                process.kill()
            process.wait(timeout=5)

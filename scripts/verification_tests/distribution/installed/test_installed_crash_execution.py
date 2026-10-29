"""Abrupt installed-engine loss uses pinned identity and never invents API completion."""
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases import crash

IDENTITY = {'pid': 123, 'parent_pid': 100, 'start_time_ticks': 42}


class InstalledCrashTests(unittest.TestCase):
    def test_sigkill_is_sent_only_to_the_captured_engine_pidfd_and_exit_is_observed(self):
        observer = Mock()
        observer.poll.return_value = [(7, 1)]
        with patch.object(crash.os, 'pidfd_open', return_value=7, create=True) as open_fd, \
                patch.object(crash.os, 'close') as close, \
                patch.object(crash, 'process_identity', return_value=IDENTITY), \
                patch.object(crash.signal, 'pidfd_send_signal', create=True) as send, \
                patch.object(crash.select, 'poll', return_value=observer, create=True):
            result = crash.kill_engine(IDENTITY)
            open_fd.assert_called_once_with(123, 0)
            send.assert_called_once_with(7, crash.signal.SIGKILL, None, 0)
            close.assert_called_once_with(7)
            self.assertTrue(result['exit_observed'])
            self.assertNotIn('exit_code', result)

    def test_reused_engine_pid_is_not_signaled(self):
        with patch.object(crash.os, 'pidfd_open', return_value=7, create=True), \
                patch.object(crash.os, 'close') as close, \
                patch.object(crash, 'process_identity', return_value=dict(IDENTITY, start_time_ticks=43)), \
                patch.object(crash.signal, 'pidfd_send_signal', create=True) as send:
            with self.assertRaises(ValueError):
                crash.kill_engine(IDENTITY)
            send.assert_not_called()
            close.assert_called_once_with(7)

    def test_signal_delivery_without_observed_engine_exit_does_not_pass(self):
        observer = Mock()
        observer.poll.return_value = []
        with patch.object(crash.os, 'pidfd_open', return_value=7, create=True), \
                patch.object(crash.os, 'close'), \
                patch.object(crash, 'process_identity', return_value=IDENTITY), \
                patch.object(crash.signal, 'pidfd_send_signal', create=True), \
                patch.object(crash.select, 'poll', return_value=observer, create=True):
            with self.assertRaises(OSError):
                crash.kill_engine(IDENTITY)

    def test_lifetime_requires_both_normal_and_crash_outcomes_and_preserves_separate_streams(self):
        for result in ('passed', 'failed'):
            with patch.object(crash, 'scenario', return_value=('passed', {'normal': True}, {'stdout': b'normal'})), \
                    patch.object(crash, 'engine_crash', return_value=(result, {'crash': True}, {'stdout': b'crash'})):
                status, facts, streams = crash.host_execution_lifetime({})
                self.assertEqual(status, result)
                self.assertEqual(facts['engine_sigkill']['status'], result)
                self.assertEqual(streams, {'normal-stdout': b'normal', 'crash-stdout': b'crash'})
        with patch.object(crash, 'scenario', return_value=('failed', {}, {})), \
                patch.object(crash, 'engine_crash') as terminate:
            self.assertEqual(crash.detached_cleanup({})[0], 'failed')
            terminate.assert_not_called()

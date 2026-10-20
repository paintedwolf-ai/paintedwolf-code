"""Lifecycle observations use structured handles and PID identities rather than output silence."""
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases import lifecycle


class InstalledLifecycleTests(unittest.TestCase):
    def test_running_handle_requires_an_invoked_command(self):
        row = {'tool': 'command', 'process': {'handle': 'command_123', 'running': True},
               'invocation': {'tool': 'command', 'invoked': True}}
        self.assertEqual(lifecycle.handle([row]), 'command_123')
        for field, value in [('handle', '../foreign'), ('running', 'true')]:
            changed = dict(row, process=dict(row['process'], **{field: value}))
            with self.subTest(field=field), self.assertRaises(ValueError):
                lifecycle.handle([changed])
        with self.assertRaises(ValueError):
            lifecycle.handle([dict(row, invocation={'tool': 'command', 'invoked': False})])
        with self.assertRaises(ValueError):
            lifecycle.handle([row, row])

    def test_mirror_requires_exact_unique_typed_process_state(self):
        result = {'process_id': 'command_123', 'running': False, 'exit_code': 0}
        for rows, accepted in [([result], True), ([], False), ([result, result], False),
                               ([dict(result, running='false')], False)]:
            app = SimpleNamespace(request=lambda *args: {'status': 200, 'body': {'processes': rows}})
            if accepted:
                self.assertEqual(lifecycle.mirror(app, 'session', 'command_123'), result)
            else:
                with self.assertRaises(ValueError):
                    lifecycle.mirror(app, 'session', 'command_123')

    def test_pid_reuse_is_never_signaled_and_cleanup_uses_a_pinned_pidfd(self):
        identity = {'pid': 123, 'start_ticks': '42'}
        waiter = Mock()
        waiter.poll.return_value = [(7, 1)]
        for state, should_signal in [(None, False), ('S', True)]:
            with self.subTest(state=state), \
                    patch.object(lifecycle.os, 'pidfd_open', return_value=7, create=True), \
                    patch.object(lifecycle.os, 'close') as close, \
                    patch.object(lifecycle.signal, 'pidfd_send_signal', create=True) as signal, \
                    patch.object(lifecycle, 'state', return_value=state), \
                    patch.object(lifecycle.select, 'poll', return_value=waiter, create=True):
                lifecycle.cleanup([identity])
                self.assertEqual(signal.called, should_signal)
                if should_signal:
                    signal.assert_called_once_with(7, lifecycle.signal.SIGKILL, None, 0)
                close.assert_called_once_with(7)

    def test_owned_process_must_report_exit_after_cleanup(self):
        waiter = Mock()
        waiter.poll.return_value = []
        with patch.object(lifecycle.os, 'pidfd_open', return_value=7, create=True), \
                patch.object(lifecycle.os, 'close'), \
                patch.object(lifecycle.signal, 'pidfd_send_signal', create=True), \
                patch.object(lifecycle, 'state', return_value='S'), \
                patch.object(lifecycle.select, 'poll', return_value=waiter, create=True):
            with self.assertRaises(OSError):
                lifecycle.cleanup([{'pid': 123, 'start_ticks': '42'}])

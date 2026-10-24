"""Shared native command capture bounds bytes while reading and settles only its child."""
import signal
import subprocess
import sys
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases import native_command as owner
from linux_release.installed_cases.records import CaseUnavailable


class NativeCommandTests(unittest.TestCase):
    def test_native_command_retains_exit_status_and_separate_streams(self):
        result = owner.command([sys.executable, '-c', "import sys;print('out');print('err',file=sys.stderr);sys.exit(3)"])
        self.assertEqual(result, (3, b'out\n', b'err\n'))

    def test_selected_environment_and_output_limit_apply_to_actual_child(self):
        result = owner.command([sys.executable, '-c', "import os;print(os.environ['FIXTURE'])"],
            environment={'FIXTURE': 'selected'}, timeout=5, limit=16)
        self.assertEqual(result, (0, b'selected\n', b''))
        launched = []
        popen = subprocess.Popen
        def capture(*args, **kwargs):
            child = popen(*args, **kwargs)
            launched.append(child)
            return child
        with patch.object(owner.subprocess, 'Popen', side_effect=capture):
            with self.assertRaisesRegex(CaseUnavailable, 'evidence-bound'):
                owner.command([sys.executable, '-c', "import os,time;os.write(1,b'x'*65);time.sleep(60)"], limit=16)
        self.assertEqual(len(launched), 1)
        self.assertIsNotNone(launched[0].returncode)
        self.assertTrue(launched[0].stdout.closed)
        self.assertTrue(launched[0].stderr.closed)

    def test_invalid_bounds_refuse_before_creating_any_child(self):
        with patch.object(owner.subprocess, 'Popen') as launch:
            for options in ({'limit': True}, {'limit': 0}, {'limit': owner.LIMIT + 1},
                            {'timeout': 0}, {'timeout': 301}, {'timeout': float('nan')}):
                with self.subTest(options=options), self.assertRaises(ValueError):
                    owner.command(['unused'], **options)
            launch.assert_not_called()

    def test_capture_deadline_settles_the_actual_owned_child(self):
        launched = []
        popen = subprocess.Popen
        def capture(*args, **kwargs):
            child = popen(*args, **kwargs)
            launched.append(child)
            return child
        with patch.object(owner.subprocess, 'Popen', side_effect=capture), patch.object(
                owner, 'remaining', side_effect=[CaseUnavailable('native-command-deadline'), 5]):
            with self.assertRaisesRegex(CaseUnavailable, 'native-command-deadline'):
                owner.command([sys.executable, '-c', 'import time;time.sleep(60)'])
        self.assertIsNotNone(launched[0].returncode)
        self.assertTrue(launched[0].stdout.closed)
        self.assertTrue(launched[0].stderr.closed)

    def test_cleanup_has_one_shared_deadline_and_retains_unsettled_identity(self):
        child = Mock(pid=123, returncode=None)
        child.wait.side_effect = subprocess.TimeoutExpired('owned', 5)
        with patch.object(owner.os, 'killpg') as kill:
            with self.assertRaises(CaseUnavailable) as refused:
                owner.settle(child)
            kill.assert_called_once_with(123, signal.SIGKILL)
        self.assertEqual(refused.exception.observations, {'native_command_cleanup': {
            'pid': 123, 'group_signalled': True, 'reaped': False, 'failure_type': 'TimeoutExpired'}})
        self.assertLessEqual(child.wait.call_args.kwargs['timeout'], owner.CLEANUP_SECONDS)
        self.assertGreater(child.wait.call_args.kwargs['timeout'], 0)

    def test_privileged_group_fallback_uses_only_captured_group_and_remaining_budget(self):
        child = Mock(pid=123, returncode=None)
        fallback = Mock(pid=124, returncode=None)
        def reaped(**_):
            fallback.returncode = 0
            return 0
        fallback.wait.side_effect = reaped
        with patch.object(owner.os, 'killpg', side_effect=PermissionError), patch.object(
                owner.subprocess, 'Popen', return_value=fallback) as launch, patch.object(
                owner.time, 'monotonic', side_effect=[10, 11, 12]):
            owner.settle(child)
        self.assertEqual(launch.call_args.args[0], ['/usr/bin/sudo', '-n', '/bin/kill', '-KILL', '--', '-123'])
        fallback.wait.assert_called_once_with(timeout=2)
        child.wait.assert_called_once_with(timeout=3)

    def test_reaped_child_group_is_never_signalled_or_waited_again(self):
        child = Mock(pid=123, returncode=0)
        with patch.object(owner.os, 'killpg') as kill, patch.object(owner.subprocess, 'Popen') as fallback:
            owner.settle(child)
        kill.assert_not_called()
        fallback.assert_not_called()
        child.wait.assert_not_called()

    def test_interrupted_wait_after_actual_reap_does_not_signal_a_reusable_pid(self):
        popen = subprocess.Popen
        launched = []
        def capture(*args, **kwargs):
            child = popen(*args, **kwargs)
            wait = child.wait
            def interrupted(**options):
                wait(**options)
                raise KeyboardInterrupt
            child.wait = interrupted
            launched.append(child)
            return child
        with patch.object(owner.subprocess, 'Popen', side_effect=capture), patch.object(owner.os, 'killpg') as kill:
            with self.assertRaises(KeyboardInterrupt):
                owner.command([sys.executable, '-c', 'pass'])
        self.assertEqual(launched[0].returncode, 0)
        kill.assert_not_called()

    def test_fallback_timeout_bounds_each_phase_and_retains_both_unsettled_children(self):
        child = Mock(pid=123, returncode=None)
        fallback = Mock(pid=124, returncode=None)
        child.wait.side_effect = subprocess.TimeoutExpired('owned', 0)
        fallback.wait.side_effect = subprocess.TimeoutExpired('helper', 1)
        with patch.object(owner.os, 'killpg', side_effect=PermissionError), patch.object(
                owner.subprocess, 'Popen', return_value=fallback), patch.object(
                owner.time, 'monotonic', side_effect=[10, 11, 12, 14, 15]):
            with self.assertRaises(CaseUnavailable) as refused:
                owner.settle(child)
        self.assertEqual([call.kwargs['timeout'] for call in fallback.wait.call_args_list], [2, 1.5, 1])
        fallback.terminate.assert_called_once()
        fallback.kill.assert_called_once()
        child.wait.assert_called_once_with(timeout=0)
        facts = refused.exception.observations['native_command_cleanup']
        self.assertEqual((facts['pid'], facts['fallback_pid']), (123, 124))
        self.assertFalse(facts['reaped'])
        self.assertFalse(facts['fallback_reaped'])

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
                            {'timeout': 0}, {'timeout': 301}, {'timeout': float('nan')},
                            {'input_bytes': b''}, {'input_bytes': 'secret'},
                            {'limit': 8, 'input_bytes': b'123456789'}):
                with self.subTest(options=options), self.assertRaises(ValueError):
                    owner.command(['unused'], **options)
            launch.assert_not_called()

    def test_input_bytes_delivers_bounded_stdin_and_closes_eof_immediately(self):
        secret = bytearray(b'pwqual058a:SecretValue0123456789abcdefABCDEF\n')
        result = owner.command(
            [sys.executable, '-c',
             "import hashlib,sys;data=sys.stdin.buffer.read();print(len(data),hashlib.sha256(data).hexdigest())"],
            input_bytes=secret, timeout=5, limit=1024)
        self.assertEqual(result[0], 0)
        self.assertEqual(result[2], b'')
        self.assertIn(b'45 ', result[1])
        self.assertNotIn(b'SecretValue0123456789abcdefABCDEF', result[1])

    def test_large_chunked_input_bytes_interleaves_with_stdout_without_deadlock(self):
        payload = (b'ChunkSecret9876543210' * 10000)
        result = owner.command(
            [sys.executable, '-c',
             "import hashlib,sys\n"
             "total = 0\n"
             "digest = hashlib.sha256()\n"
             "while chunk := sys.stdin.buffer.read(16384):\n"
             "    total += len(chunk)\n"
             "    digest.update(chunk)\n"
             "    sys.stdout.buffer.write(b'.')\n"
             "    sys.stdout.buffer.flush()\n"
             "print(f'\\n{total}:{digest.hexdigest()}')\n"],
            input_bytes=payload, timeout=10, limit=len(payload) + 4096)
        self.assertEqual(result[0], 0)
        self.assertIn(f'\n{len(payload)}:'.encode('ascii'), result[1])
        self.assertNotIn(b'ChunkSecret9876543210', result[1])

    def test_early_child_exit_or_broken_pipe_refuses_without_leaking_input_bytes(self):
        secret = b'TopSecretCredentialForBrokenPipeTest0123456789' * 4000
        launched = []
        popen = subprocess.Popen
        def capture(*args, **kwargs):
            child = popen(*args, **kwargs)
            launched.append(child)
            return child
        with patch.object(owner.subprocess, 'Popen', side_effect=capture):
            with self.assertRaisesRegex(CaseUnavailable, 'native-command-stdin-incomplete') as refused:
                owner.command([sys.executable, '-c', 'import sys;sys.stdin.close();sys.exit(0)'],
                              input_bytes=secret, timeout=5, limit=len(secret) + 1024)
        self.assertEqual(len(launched), 1)
        self.assertIsNotNone(launched[0].returncode)
        self.assertTrue(launched[0].stdin.closed)
        observations = getattr(refused.exception, 'observations', {})
        self.assertTrue(observations['native_command_stdin']['stdin_closed'])
        self.assertEqual(observations['native_command_stdin']['stdin_bytes_expected'], len(secret))
        self.assertLess(observations['native_command_stdin']['stdin_bytes_written'], len(secret))
        self.assertNotIn('TopSecretCredentialForBrokenPipeTest0123456789', repr(refused.exception))
        self.assertNotIn('TopSecretCredentialForBrokenPipeTest0123456789', repr(observations))

        launched.clear()
        with patch.object(owner.subprocess, 'Popen', side_effect=capture), patch.object(
                owner.os, 'write', side_effect=BrokenPipeError):
            with self.assertRaisesRegex(CaseUnavailable, 'native-command-stdin-incomplete') as refused:
                owner.command([sys.executable, '-c', 'import time;time.sleep(60)'],
                              input_bytes=secret, timeout=5, limit=len(secret) + 1024)
        self.assertEqual(len(launched), 1)
        self.assertIsNotNone(launched[0].returncode)
        self.assertTrue(launched[0].stdin.closed)
        observations = getattr(refused.exception, 'observations', {})
        self.assertEqual(observations['native_command_stdin'], {
            'stdin_closed': True, 'stdin_bytes_written': 0, 'stdin_bytes_expected': len(secret)})
        self.assertNotIn('TopSecretCredentialForBrokenPipeTest0123456789', repr(refused.exception))
        self.assertNotIn('TopSecretCredentialForBrokenPipeTest0123456789', repr(observations))

    def test_timeout_with_input_bytes_closes_stdin_settles_child_and_never_leaks_secret(self):
        secret = b'NeverLeakThisTimeoutSecretValue0123456789ABCD'
        launched = []
        popen = subprocess.Popen
        def capture(*args, **kwargs):
            child = popen(*args, **kwargs)
            launched.append(child)
            return child
        with patch.object(owner.subprocess, 'Popen', side_effect=capture), patch.object(
                owner, 'remaining', side_effect=[5, CaseUnavailable('native-command-deadline'), 5]):
            with self.assertRaisesRegex(CaseUnavailable, 'native-command-deadline') as refused:
                owner.command([sys.executable, '-c', 'import sys,time;sys.stdin.buffer.read(1);time.sleep(60)'],
                              input_bytes=secret, timeout=5, limit=1024)
        self.assertEqual(len(launched), 1)
        self.assertIsNotNone(launched[0].returncode)
        self.assertTrue(launched[0].stdin.closed)
        self.assertTrue(launched[0].stdout.closed)
        self.assertTrue(launched[0].stderr.closed)
        self.assertNotIn('NeverLeakThisTimeoutSecretValue0123456789ABCD', repr(refused.exception))
        self.assertNotIn('NeverLeakThisTimeoutSecretValue0123456789ABCD',
                         repr(getattr(refused.exception, 'observations', None)))

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

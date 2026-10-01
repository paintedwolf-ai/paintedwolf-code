import fcntl
import json
import os
import pathlib
import signal
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import Mock, patch
from episode import disposition, reattachment_order
from episode_runtime import execute, lease_available, worker, command_worker, assert_execution_quiescence, run_retained_step, RetainedProcessingExhausted
from process_group import live_members, signal_group, stop_process
from measurement_cache import measure
from progress import save


def await_path(path):
    deadline = time.monotonic() + 15
    while not path.exists():
        if time.monotonic() > deadline:
            raise AssertionError('fixture worker did not write ' + str(path))
        time.sleep(0.02)


def run_command_worker(root):
    reader, writer = os.pipe()
    try:
        command_worker(root, reader)
    finally:
        os.close(reader)
        os.close(writer)


class EpisodeRuntimeTests(unittest.TestCase):
    def test_cleanup_failure_retains_unknown_execution_without_replay(self):
        for failure in [PermissionError(1, 'denied'), TimeoutError('group remains live')]:
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                command = ['fixture-command']
                save(root / 'command.json', {'command': command, 'cwd': str(root)})
                process = Mock(pid=41, returncode=37)
                process.poll.return_value = 37
                with patch('episode_runtime.subprocess.Popen', return_value=process) as launch, \
                     patch('episode_runtime.stop_process', side_effect=failure) as stop:
                    run_command_worker(root)
                    result = execute(root, command, root, threading.Event())
                    self.assertEqual(result, {'kind': 'execution_unknown', 'returncode': 37})
                    self.assertEqual(disposition(result, {'cases': [{'status': 'failed'}]}),
                                     ('blocked', 'execution_unknown'))
                    launch.assert_called_once()
                    stop.assert_called_once_with(process)
                self.assertTrue((root / 'cleanup-error.txt').exists())

    def test_published_completion_is_not_replaced_after_a_directory_sync_error(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            command = [sys.executable, '-c', 'pass']
            save(root / 'command.json', {'command': command, 'cwd': str(root)})
            def interrupted_save(path, value):
                save(path, value)
                if path.name == 'exit.json':
                    raise OSError('directory sync failed after atomic publication')
            with patch('episode_runtime.save', side_effect=interrupted_save):
                with self.assertRaises(OSError):
                    run_command_worker(root)
            self.assertFalse((root / 'worker-error.txt').exists())
            self.assertEqual(execute(root, command, root, threading.Event()), {'kind': 'exited', 'returncode': 0})

    def test_detached_daemon_does_not_inherit_execution_lease(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            (root / 'daemon.py').write_text(
                'import json, os, pathlib, time\n'
                'p=pathlib.Path.cwd(); target=os.stat(p/"lease"); inherited=[]\n'
                'for name in os.listdir("/dev/fd"):\n'
                ' try: info=os.fstat(int(name))\n'
                ' except OSError: continue\n'
                ' if (info.st_dev,info.st_ino)==(target.st_dev,target.st_ino): inherited.append(int(name))\n'
                '(p/"daemon-ready").write_text(json.dumps(inherited))\n'
                'while not (p/"release").exists(): time.sleep(.02)\n'
                '(p/"daemon-stopped").touch()\n')
            (root / 'launcher.py').write_text(
                'import pathlib, subprocess, sys, time\n'
                'subprocess.Popen([sys.executable,"daemon.py"],start_new_session=True,close_fds=False)\n'
                'while not pathlib.Path("daemon-ready").exists(): time.sleep(.02)\n')
            save(root / 'command.json', {'command': [sys.executable, str(root / 'launcher.py')],
                                         'cwd': str(root)})
            try:
                worker(root)
                self.assertEqual(json.loads((root / 'daemon-ready').read_text()), [])
                self.assertTrue(lease_available(root), 'detached daemon retained supervisor lock')
                result = execute(root, [sys.executable, str(root / 'launcher.py')], root, threading.Event())
                self.assertEqual(result, {'kind': 'exited', 'returncode': 0})
                self.assertFalse((root / 'daemon-stopped').exists(), 'finalization must not kill detached daemon')
            finally:
                (root / 'release').touch()
                if (root / 'daemon-ready').exists():
                    await_path(root / 'daemon-stopped')

    def test_supervisor_crash_drains_command_before_releasing_admission(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            (root / 'command.py').write_text(
                'import pathlib,time\np=pathlib.Path.cwd()\n'
                'with (p/"calls").open("a") as f: f.write("called\\n")\n'
                'while not (p/"release").exists(): time.sleep(.02)\n'
                '(p/"command-stopped").touch()\n')
            command = [sys.executable, str(root / 'command.py')]
            save(root / 'command.json', {'command': command, 'cwd': str(root)})
            supervisor = subprocess.Popen([sys.executable, str(pathlib.Path(__file__).with_name('episode_runtime.py')),
                                           '--worker', str(root)], start_new_session=True)
            try:
                await_path(root / 'calls')
                supervisor.kill()
                supervisor.wait(timeout=5)
                result = execute(root, command, root, threading.Event())
                self.assertEqual(result['kind'], 'interrupted')
                self.assertEqual(disposition(result, None), ('blocked', 'execution_interrupted'))
                self.assertEqual((root / 'calls').read_text(), 'called\n')
                process = json.loads((root / 'process.json').read_text())
                self.assertFalse(live_members(process['pid']))
                self.assertTrue(lease_available(root))
            finally:
                (root / 'release').touch()
                if supervisor.poll() is None:
                    supervisor.kill(); supervisor.wait(timeout=5)
                if (root / 'process.json').exists():
                    signal_group(json.loads((root / 'process.json').read_text())['pid'], signal.SIGKILL)

    def test_graceful_pause_drains_admitted_command_without_replaying(self):
        from concurrent.futures import ThreadPoolExecutor
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            script = root / 'command.py'
            script.write_text('import pathlib,time\np=pathlib.Path.cwd()\n(p/"started").touch()\n'
                              'while not (p/"release").exists(): time.sleep(.02)\n(p/"completed").touch()\n')
            command = [sys.executable, str(script)]
            cancelled = threading.Event()
            with ThreadPoolExecutor(max_workers=1) as pool:
                future = pool.submit(execute, root, command, root, cancelled)
                try:
                    await_path(root / 'started')
                    cancelled.set()
                    time.sleep(.3)
                    self.assertFalse(future.done())
                finally:
                    (root / 'release').touch()
                result = future.result(timeout=15)
            self.assertEqual(result, {'kind': 'exited', 'returncode': 0})
            self.assertTrue((root / 'completed').exists())
            self.assertEqual(execute(root, command, root, threading.Event()), result)

    def test_unknown_execution_prevents_admission_after_restart(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            for directory in ['captures/episode-000/attempt-0000', 'provider-preflight/model/attempt-0000']:
                attempt = root / directory
                attempt.mkdir(parents=True)
                save(attempt / 'exit.json', {'kind': 'execution_unknown', 'returncode': None})
                with self.assertRaisesRegex(RuntimeError, 'cleanup is unconfirmed'):
                    assert_execution_quiescence(root)
                save(attempt / 'exit.json', {'kind': 'exited', 'returncode': 0})
            assert_execution_quiescence(root)

    def test_released_launch_lease_blocks_resume_before_exit_receipt(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            attempt = root / 'captures/episode-000/attempt-0000'
            attempt.mkdir(parents=True)
            save(attempt / 'launched.json', {'started_at': 'fixture'})
            with (attempt / 'lease').open('a') as lease:
                fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
                assert_execution_quiescence(root)
            with self.assertRaisesRegex(RuntimeError, 'cleanup is unconfirmed'):
                assert_execution_quiescence(root)
            save(attempt / 'exit.json', {'kind': 'exited', 'returncode': 0})
            assert_execution_quiescence(root)

    def test_finished_launcher_releases_descendant_lease_without_changing_result(self):
        for ignore_term in (False, True):
            with self.subTest(ignore_term=ignore_term), tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                child = root / 'child.py'
                child.write_text(
                    'import pathlib, signal, time\n'
                    + ('signal.signal(signal.SIGTERM, signal.SIG_IGN)\n' if ignore_term else '')
                    + 'pathlib.Path("ready").touch()\nwhile True: time.sleep(.02)\n')
                launcher = root / 'launcher.py'
                launcher.write_text(
                    'import pathlib, subprocess, sys, time\n'
                    'subprocess.Popen([sys.executable, "child.py"], close_fds=False)\n'
                    'while not pathlib.Path("ready").exists(): time.sleep(.02)\n'
                    'sys.exit(37)\n')
                save(root / 'command.json', {'command': [sys.executable, str(launcher)], 'cwd': str(root)})
                try:
                    with patch('episode_runtime.stop_process', side_effect=lambda p: stop_process(p, .1)):
                        run_command_worker(root)
                    deadline = time.monotonic() + 5
                    while not lease_available(root) and time.monotonic() < deadline:
                        time.sleep(.02)
                    self.assertTrue(lease_available(root), 'completed command left an inherited lease open')
                    self.assertFalse(live_members(json.loads((root / 'process.json').read_text())['pid']))
                    error = root / 'worker-error.txt'
                    self.assertEqual(json.loads((root / 'exit.json').read_text()),
                                     {'kind': 'exited', 'returncode': 37},
                                     error.read_text() if error.exists() else 'launcher result changed')
                finally:
                    receipt = root / 'process.json'
                    if receipt.exists():
                        signal_group(json.loads(receipt.read_text())['pid'], signal.SIGKILL)

    def test_completed_receipt_does_not_wait_for_a_retained_lock(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            ended = {'kind': 'exited', 'returncode': 0}
            save(root / 'exit.json', ended)
            cancelled = threading.Event()
            cancelled.set()
            with (root / 'lease').open('a') as lease:
                fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
                result = execute(root, ['must-not-launch'], root, cancelled)
                self.assertEqual(result, ended)
            self.assertEqual(json.loads((root / 'exit.json').read_text()), ended)
            self.assertFalse((root / 'launched.json').exists())

    def test_controller_crash_rejoins_original_execution_without_replaying(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            directory = root / 'attempt'
            script = root / 'command.py'
            script.write_text("from pathlib import Path\nimport time\np=Path(__file__).parent\nwith (p/'calls').open('a') as f: f.write('called\\n')\nwhile not (p/'release').exists(): time.sleep(.02)\n")
            command = [sys.executable, str(script)]
            controller = subprocess.Popen([sys.executable, '-c',
                'from episode_runtime import execute; import pathlib, threading, json, sys; execute(pathlib.Path(sys.argv[1]), json.loads(sys.argv[2]), pathlib.Path(sys.argv[3]), threading.Event())',
                str(directory), json.dumps(command), str(root)], cwd=pathlib.Path(__file__).parent)
            try:
                await_path(root / 'calls')
                controller.kill()
                controller.wait(timeout=5)
                (root / 'release').touch()
                result = execute(directory, command, root, threading.Event())
                self.assertEqual(result, {'kind': 'exited', 'returncode': 0})
                self.assertEqual((root / 'calls').read_text(), 'called\n')
            finally:
                (root / 'release').touch()
                if controller.poll() is None:
                    controller.kill(); controller.wait()
                if directory.exists():
                    await_path(directory / 'exit.json')

    def test_surviving_execution_reserves_capacity_before_new_admission(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            attempt = root / 'captures/episode-002/attempt-0000'
            attempt.mkdir(parents=True)
            pending = [{'index': index} for index in range(4)]
            with (attempt / 'lease').open('a') as lease:
                fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
                self.assertEqual([item['index'] for item in reattachment_order(root, pending)], [2, 0, 1, 3])
            self.assertEqual(reattachment_order(root, pending), pending)

    def test_resume_rejects_a_different_command_before_launch(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            save(root / 'command.json', {'command': ['original'], 'cwd': str(root)})
            with self.assertRaisesRegex(ValueError, 'retained admission'):
                execute(root, ['different'], root, threading.Event())
            self.assertFalse((root / 'launched.json').exists())

    def test_ambiguous_launch_does_not_repeat_the_command(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            save(root / 'command.json', {'command': ['must-not-launch'], 'cwd': str(root)})
            save(root / 'launched.json', {'started_at': 'fixture'})
            worker(root)
            self.assertEqual(json.loads((root / 'exit.json').read_text())['kind'], 'execution_unknown')

    def test_unknown_execution_never_replays_or_measures_a_possibly_live_report(self):
        for report in (None, {'cases': [{'status': 'failed'}]},
                       {'cases': [{'status': 'error', 'failure': {
                           'kind': 'provider', 'code': 'provider_overloaded', 'retryable': True}}]}):
            with self.subTest(report=report):
                self.assertEqual(disposition({'kind': 'execution_unknown', 'returncode': None}, report),
                                 ('blocked', 'execution_unknown'))

    def test_pause_before_launch_resumes_one_original_command(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            command = [sys.executable, '-c',
                       'from pathlib import Path; p=Path("calls"); p.write_text(p.read_text()+"called\\n" if p.exists() else "called\\n")']
            cancelled = threading.Event()
            cancelled.set()
            with self.assertRaisesRegex(InterruptedError, 'paused before launch'):
                execute(root, command, root, cancelled)
            for name in ['calls', 'launched.json', 'exit.json']:
                self.assertFalse((root / name).exists())
            cancelled.clear()
            result = execute(root, command, root, cancelled)
            self.assertEqual(result, {'kind': 'exited', 'returncode': 0})
            self.assertEqual(execute(root, command, root, cancelled), result)
            self.assertEqual((root / 'calls').read_text(), 'called\n')

    def test_pause_after_supervisor_spawn_waits_for_its_receipt(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            cancelled = threading.Event()
            process = Mock()
            process.poll.return_value = None
            sleeps = []
            def launch(*args, **kwargs):
                cancelled.set()
                return process
            def tick(*args):
                sleeps.append(True)
                if len(sleeps) == 2:
                    save(root / 'exit.json', {'kind': 'exited', 'returncode': 0})
            with patch('episode_runtime.subprocess.Popen', side_effect=launch) as spawned, \
                 patch('episode_runtime.time.sleep', side_effect=tick):
                result = execute(root, ['command'], root, cancelled)
            self.assertEqual(result, {'kind': 'exited', 'returncode': 0})
            spawned.assert_called_once()
            self.assertEqual(len(sleeps), 2)

    def test_committed_exit_reaps_owned_supervisor_before_returning(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            process = Mock()
            process.poll.return_value = None
            result = {'kind': 'exited', 'returncode': 0}
            def settled(_):
                save(root / 'exit.json', result)
            with patch('episode_runtime.subprocess.Popen', return_value=process), \
                    patch('episode_runtime.time.sleep', side_effect=settled):
                self.assertEqual(execute(root, ['command'], root, threading.Event()), result)
            process.wait.assert_called_once_with()
            process.terminate.assert_not_called()
            process.kill.assert_not_called()

    def test_controller_failure_reaps_later_without_stopping_live_execution(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            process = Mock()
            process.poll.return_value = None
            reaping, release, reaped = threading.Event(), threading.Event(), threading.Event()
            def wait():
                reaping.set()
                release.wait(5)
                reaped.set()
                return 0
            process.wait.side_effect = wait
            try:
                with patch('episode_runtime.subprocess.Popen', return_value=process), \
                        patch('episode_runtime.time.sleep', side_effect=OSError('controller observation failed')):
                    with self.assertRaisesRegex(OSError, 'controller observation failed'):
                        execute(root, ['command'], root, threading.Event())
                self.assertTrue(reaping.wait(1))
                self.assertFalse(reaped.is_set())
                process.terminate.assert_not_called()
                process.kill.assert_not_called()
            finally:
                release.set()
                self.assertTrue(reaped.wait(1))
            process.wait.assert_called_once_with()

    def test_only_typed_infrastructure_faults_allow_replacement(self):
        ended = {'kind': 'exited', 'returncode': 1}
        for status in ['review_required', 'failed', 'interaction_exhausted']:
            case = {'status': status}
            if status != 'review_required':
                case['failure'] = {'kind': 'model', 'code': 'missing_closeout', 'retryable': False}
            self.assertEqual(disposition(ended, {'cases': [case]})[0], 'measured')
        self.assertEqual(disposition(ended, {'cases': [{'status': 'error', 'error': 'provider overloaded'}]})[0], 'blocked')
        fault = {'kind': 'provider', 'code': 'provider_overloaded', 'retryable': True}
        self.assertEqual(disposition(ended, {'cases': [{'status': 'failed', 'failure': fault}]}), ('blocked', 'execution_contract_invalid'))
        self.assertEqual(disposition(ended, {'cases': [{'status': 'failed'}]}), ('blocked', 'execution_contract_invalid'))
        self.assertEqual(disposition(ended, {'cases': [{'status': 'error', 'failure': fault, 'task_allowance': {'completed': 0}}]}), ('retry', 'provider_overloaded'))
        setup = {'kind': 'harness', 'code': 'preparation_transport', 'retryable': True}
        self.assertEqual(disposition(ended, {'cases': [{'status': 'error', 'failure': setup}]}), ('retry', 'preparation_transport'))
        setup['code'] = 'unknown'
        self.assertEqual(disposition(ended, {'cases': [{'status': 'error', 'failure': setup}]})[0], 'blocked')
        mismatch = {'failure': {'kind': 'harness', 'code': 'configuration_mismatch', 'retryable': False}}
        self.assertEqual(disposition(ended, None, mismatch), ('blocked', 'configuration_mismatch'))
        fixture = {'kind': 'harness', 'code': 'fixture_preparation', 'retryable': False}
        self.assertEqual(disposition(ended, {'cases': [{'status': 'error', 'failure': fixture}]}),
                         ('blocked', 'fixture_preparation'))
        fault['retryable'] = False
        self.assertEqual(disposition(ended, {'cases': [{'status': 'error', 'failure': fault}]})[0], 'blocked')

    def test_grading_resume_reuses_only_matching_complete_evidence(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            evidence = root / 'input'
            evidence.write_text('one')
            evaluate = Mock(return_value={'outcome': 'passed'})
            self.assertEqual(measure(root, 'grader', [evidence], evaluate), {'outcome': 'passed'})
            measure(root, 'grader', [evidence], evaluate)
            self.assertEqual(evaluate.call_count, 1)
            evidence.write_text('two')
            measure(root, 'grader', [evidence], evaluate)
            measure(root, 'new grader', [evidence], evaluate)
            self.assertEqual(evaluate.call_count, 3)
            evaluate.side_effect = OSError('grader offline')
            with self.assertRaises(OSError): measure(root, 'third grader', [evidence], evaluate)
            evaluate.side_effect = None
            measure(root, 'third grader', [evidence], evaluate)
            self.assertEqual(evaluate.call_count, 5)


class RetainedProcessingTests(unittest.TestCase):
    def test_completed_processing_is_reused_without_another_command(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            command = [sys.executable, '-c', 'from pathlib import Path; p=Path("calls"); p.write_text(p.read_text()+"x" if p.exists() else "x")']
            run_retained_step(root / 'finalization', command, root, threading.Event())
            run_retained_step(root / 'finalization', command, root, threading.Event())
            self.assertEqual((root / 'calls').read_text(), 'x')
            self.assertEqual(len(list((root / 'finalization').glob('attempt-*'))), 1)

    def test_controller_crash_reattaches_to_one_finalization(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            script = root / 'refresh.py'
            script.write_text('from pathlib import Path\nimport time\np=Path.cwd()\n'
                              'with (p/"calls").open("a") as f: f.write("x")\n'
                              'while not (p/"release").exists(): time.sleep(.02)\n')
            directory = root / 'finalization'
            command = [sys.executable, str(script)]
            controller = subprocess.Popen([sys.executable, '-c',
                'from episode_runtime import run_retained_step; import pathlib,threading,json,sys; '
                'run_retained_step(pathlib.Path(sys.argv[1]),json.loads(sys.argv[2]),pathlib.Path(sys.argv[3]),threading.Event())',
                str(directory), json.dumps(command), str(root)], cwd=pathlib.Path(__file__).parent)
            try:
                await_path(root / 'calls')
                controller.kill()
                controller.wait(timeout=5)
                (root / 'release').touch()
                run_retained_step(directory, command, root, threading.Event())
                self.assertEqual((root / 'calls').read_text(), 'x')
                self.assertEqual(len(list(directory.glob('attempt-*'))), 1)
            finally:
                (root / 'release').touch()
                if controller.poll() is None:
                    controller.kill(); controller.wait(timeout=5)
                if directory.exists():
                    await_path(directory / 'attempt-0000/exit.json')

    def test_interrupted_finalization_retries_without_replacing_candidate(self):
        from concurrent.futures import ThreadPoolExecutor
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            candidate = {'kind': 'exited', 'returncode': 0}
            save(root / 'exit.json', candidate)
            directory = root / 'finalization'
            script = root / 'refresh.py'
            script.write_text('from pathlib import Path\nimport time\np=Path.cwd()\n'
                              'with (p/"calls").open("a") as f: f.write("x")\n'
                              'while (p/"calls").read_text()=="x": time.sleep(.02)\n')
            command = [sys.executable, str(script)]
            with ThreadPoolExecutor(max_workers=1) as pool:
                future = pool.submit(run_retained_step, directory, command, root, threading.Event())
                try:
                    await_path(root / 'calls')
                    supervisor = json.loads((directory / 'attempt-0000/launched.json').read_text())['worker_pid']
                    os.kill(supervisor, signal.SIGKILL)
                    future.result(timeout=15)
                finally:
                    if not future.done():
                        (root / 'calls').write_text('released')
            original = (directory / 'attempt-0000/exit.json').read_bytes()
            run_retained_step(directory, command, root, threading.Event())
            self.assertEqual((root / 'calls').read_text(), 'xx')
            self.assertEqual(json.loads((root / 'exit.json').read_text()), candidate)
            self.assertEqual((directory / 'attempt-0000/exit.json').read_bytes(), original)
            self.assertEqual(len(list(directory.glob('attempt-*'))), 2)

    def test_retained_retry_accounting_survives_pause_and_exhaustion(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            cancelled = Mock()
            cancelled.is_set.return_value = False
            cancelled.wait.return_value = True
            calls = []
            def execute(attempt, *args):
                calls.append(attempt.name)
                attempt.mkdir(parents=True, exist_ok=True)
                result = {'kind':'exited', 'returncode':7}
                save(attempt / 'exit.json', result)
                return result
            with patch('recovery.MAX_INFRASTRUCTURE_ATTEMPTS', 3), \
                 patch('episode_runtime.execute', side_effect=execute):
                with self.assertRaisesRegex(InterruptedError, 'processing paused'):
                    run_retained_step(root, ['refresh'], root, cancelled)
                first = (root / 'attempt-0000/disposition.json').read_bytes()
                self.assertEqual(calls, ['attempt-0000'])
                self.assertGreater(cancelled.wait.call_args.args[0], 0)
                cancelled.wait.return_value = False
                with self.assertRaises(RetainedProcessingExhausted) as exhausted:
                    run_retained_step(root, ['refresh'], root, cancelled)
                self.assertEqual(len(exhausted.exception.recovery['attempts']), 3)
                self.assertTrue(exhausted.exception.recovery['exhausted'])
                self.assertEqual(calls, ['attempt-0000','attempt-0001','attempt-0002'])
                with self.assertRaises(RetainedProcessingExhausted):
                    run_retained_step(root, ['refresh'], root, cancelled)
                self.assertEqual(len(calls), 3)
                self.assertEqual((root / 'attempt-0000/disposition.json').read_bytes(), first)

    def test_pause_before_retained_launch_consumes_no_failure_attempt(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            cancelled = threading.Event()
            cancelled.set()
            with self.assertRaises(InterruptedError):
                run_retained_step(root, ['refresh'], root, cancelled)
            self.assertEqual(list(root.glob('attempt-*/disposition.json')), [])
            self.assertEqual(list(root.glob('attempt-*/launched.json')), [])
            self.assertEqual(list(root.glob('attempt-*/exit.json')), [])

    def test_unknown_finalization_cannot_start_another_writer(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            attempt = root / 'captures/episode-000/attempt-0000/finalization/attempt-0000'
            attempt.mkdir(parents=True)
            save(attempt / 'exit.json', {'kind': 'execution_unknown', 'returncode': None})
            with patch('episode_runtime.execute') as launch:
                with self.assertRaisesRegex(RuntimeError, 'cleanup is unconfirmed'):
                    run_retained_step(attempt.parent, ['refresh'], root, threading.Event())
                launch.assert_not_called()
            with self.assertRaisesRegex(RuntimeError, 'cleanup is unconfirmed'):
                assert_execution_quiescence(root)


if __name__ == '__main__':
    unittest.main()

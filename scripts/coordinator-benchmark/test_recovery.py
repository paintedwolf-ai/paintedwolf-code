import json
import errno
import os
import signal
import sys
import time
import pathlib
import subprocess
import tempfile
import threading
import unittest
import urllib.error
from types import SimpleNamespace
from unittest.mock import Mock, patch
import preparation
from recovery import GradingExhausted, grade_once, grading, infrastructure_recovery, local_probe
from episode_runtime import assert_execution_quiescence
from snapshot import file_hash
from progress import Progress, save


def finished_report(command, returncode=0, **kwargs):
    status_path = pathlib.Path(command[-1])
    report_path = status_path.with_name('report.json')
    save(report_path, {'mode': 'exploration', 'run_id': 'graded'})
    save(status_path, {'phase': 'completed' if returncode == 0 else 'blocked', 'retryable': bool(returncode),
                      'report_id': 'graded', 'report_path': str(report_path.resolve()),
                      'report_sha256': file_hash(report_path)})
    return subprocess.CompletedProcess(command, returncode)


class RecoveryTests(unittest.TestCase):
    def test_preparation_stage_records_failure_without_losing_the_exception(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            path = root / 'preparation-status.json'
            with self.assertRaises(subprocess.CalledProcessError):
                with preparation.stage(root, 'build'):
                    self.assertEqual(json.loads(path.read_text())['state'], 'running')
                    raise subprocess.CalledProcessError(17, ['build'])
            receipt = json.loads(path.read_text())
            self.assertEqual((receipt['phase'], receipt['state'], receipt['returncode']), ('build', 'failed', 17))
            with preparation.stage(root, 'build'):
                pass
            self.assertEqual(json.loads(path.read_text())['state'], 'completed')
            self.assertNotIn('error_type', json.loads(path.read_text()))

    def test_only_transient_metadata_errors_retry(self):
        cancelled = Mock()
        cancelled.is_set.return_value = False
        cancelled.wait.return_value = False
        operation = Mock(side_effect=[urllib.error.URLError('offline'), {'identity': 'same'}])
        self.assertEqual(local_probe(operation, cancelled), {'identity': 'same'})
        operation.side_effect = urllib.error.HTTPError('https://fixture', 401, 'no auth', {}, None)
        with self.assertRaises(urllib.error.HTTPError): local_probe(operation, cancelled)
        self.assertEqual(operation.call_count, 3)

    @staticmethod
    def execute_grader(directory, command, cwd, cancelled):
        directory.mkdir(parents=True, exist_ok=True)
        path = directory / 'exit.json'
        if path.exists():
            return json.loads(path.read_text())
        if cancelled.is_set():
            raise InterruptedError('paused')
        result = {'kind': 'exited', 'returncode': grade_once(directory)}
        save(path, result)
        return result

    def test_exited_report_requires_a_terminal_receipt_and_completed_output(self):
        for child_code in (0, 1):
            for invalid in ('missing', 'running', 'completed_without_report', 'malformed', 'changed_report'):
                with self.subTest(child_code=child_code, invalid=invalid), tempfile.TemporaryDirectory() as temporary:
                    root = pathlib.Path(temporary)
                    def report(command, **kwargs):
                        path = pathlib.Path(command[-1])
                        if invalid == 'missing':
                            path.unlink()
                        elif invalid == 'malformed':
                            path.write_text('{')
                        elif invalid == 'changed_report':
                            finished_report(command, child_code)
                            path.with_name('report.json').write_text('{}')
                        else:
                            save(path, {'phase': 'running' if invalid == 'running' else 'completed'})
                        return subprocess.CompletedProcess(command, child_code)
                    with patch('recovery.subprocess.run', side_effect=report) as invoke, \
                         patch('episode_runtime.execute', side_effect=self.execute_grader):
                        for _ in range(2):
                            with self.assertRaises(subprocess.CalledProcessError) as caught:
                                grading(['frozen-report'], root, Mock(), threading.Event())
                            self.assertNotEqual(caught.exception.returncode, 0)
                    invoke.assert_called_once()
                    status = json.loads((root / 'grading-status.json').read_text())
                    self.assertEqual(status['phase'], 'blocked')
                    self.assertFalse(status['retryable'])
                    self.assertEqual(status['failure']['code'], 'grading_receipt_invalid')
                    self.assertNotIn('report_sha256', status)

    def test_grader_service_retry_does_not_invoke_models(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            cancelled = Mock()
            cancelled.is_set.return_value = False
            cancelled.wait.return_value = False
            outcomes = iter([1, 0])
            def report(command, **kwargs):
                code = next(outcomes)
                return finished_report(command, code)
            with patch('recovery.subprocess.run', side_effect=report) as invoke, \
                    patch('episode_runtime.execute', side_effect=self.execute_grader):
                self.assertTrue(grading(['frozen-report'], root, Mock(), cancelled))
                self.assertTrue(grading(['frozen-report'], root, Mock(), cancelled))
            self.assertEqual(invoke.call_count, 2)
            self.assertTrue(all(call.args[0][:2] == ['frozen-report', '--status-out'] for call in invoke.call_args_list))
            self.assertEqual(len(infrastructure_recovery(root / 'grading')['attempts']), 1)

    def test_grading_launch_failure_recovers_without_replaying_a_completed_report(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            cancelled = Mock(is_set=Mock(return_value=False), wait=Mock(return_value=False))
            calls = 0
            def report(command, **kwargs):
                nonlocal calls
                calls += 1
                if calls == 1:
                    raise BlockingIOError(errno.EAGAIN, 'process admission unavailable')
                return finished_report(command, **kwargs)
            with patch('recovery.subprocess.run', side_effect=report) as invoke, \
                 patch('episode_runtime.execute', side_effect=self.execute_grader):
                self.assertTrue(grading(['frozen-report'], root, Mock(), cancelled))
                self.assertTrue(grading(['frozen-report'], root, Mock(), cancelled))
            self.assertEqual(invoke.call_count, 2)
            first = json.loads((root / 'grading/attempt-0000/verdict.json').read_text())
            self.assertIsNone(first['report_returncode'])
            self.assertEqual(first['returncode'], 1)
            self.assertEqual(first['status']['failure']['code'], 'grading_launch_failed')
            self.assertEqual(first['status']['failure']['errno'], errno.EAGAIN)
            self.assertNotIn('report_sha256', first['status'])

    def test_missing_grader_launch_exhausts_durably_after_restart(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            command = [str(root / 'missing-executable')]
            cancelled = Mock(is_set=Mock(return_value=False), wait=Mock(return_value=True))
            with patch('recovery.MAX_INFRASTRUCTURE_ATTEMPTS', 3), \
                 patch('episode_runtime.execute', side_effect=self.execute_grader):
                self.assertFalse(grading(command, root, Mock(), cancelled))
                first = (root / 'grading/attempt-0000/verdict.json').read_bytes()
                cancelled.wait.return_value = False
                for _ in range(2):
                    with self.assertRaises(GradingExhausted):
                        grading(command, root, Mock(), cancelled)
                self.assertEqual((root / 'grading/attempt-0000/verdict.json').read_bytes(), first)
            attempts = list((root / 'grading').glob('attempt-*'))
            self.assertEqual(len(attempts), 3)
            for attempt in attempts:
                status = json.loads((attempt / 'grading-status.json').read_text())
                self.assertEqual(status['phase'], 'blocked')
                self.assertEqual(status['failure']['errno'], errno.ENOENT)
                self.assertNotIn('report_sha256', status)
            final = json.loads((root / 'grading-status.json').read_text())
            self.assertEqual(final['phase'], 'completed_with_unmeasured')
            self.assertFalse(final['retryable'])
            self.assertTrue(final['recovery']['exhausted'])

    def test_stale_retry_receipt_cannot_replay_a_new_grader_failure(self):
        for prior_failure in (False, True):
            with self.subTest(prior_failure=prior_failure), tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                save(root / 'grading-status.json', {'phase': 'blocked', 'retryable': True})
                cancelled = Mock()
                cancelled.is_set.return_value = False
                cancelled.wait.return_value = False
                calls = []
                def report(command, **kwargs):
                    calls.append(command)
                    if prior_failure and len(calls) == 1:
                        save(pathlib.Path(command[-1]), {'phase': 'blocked', 'retryable': True})
                    elif len(calls) > 1 + int(prior_failure):
                        raise AssertionError('replayed stale retry authorization')
                    return subprocess.CompletedProcess(command, 1)
                with patch('recovery.subprocess.run', side_effect=report), \
                        patch('episode_runtime.execute', side_effect=self.execute_grader):
                    with self.assertRaises(subprocess.CalledProcessError):
                        grading(['frozen-report'], root, Mock(), cancelled)
                self.assertEqual(len(calls), 1 + int(prior_failure))

    def test_unrelated_grader_status_cannot_authorize_retry(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            def report(command, **kwargs):
                save(root / 'grading-status.json', {'phase': 'blocked', 'retryable': True})
                return subprocess.CompletedProcess(command, 1)
            with patch('recovery.subprocess.run', side_effect=report) as invoke, \
                    patch('episode_runtime.execute', side_effect=self.execute_grader):
                with self.assertRaises(subprocess.CalledProcessError):
                    grading(['frozen-report'], root, Mock(), threading.Event())
            invoke.assert_called_once()
            self.assertEqual(infrastructure_recovery(root / 'grading')['attempts'], [])

    def test_permanent_grading_error_does_not_retry(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            with patch('recovery.subprocess.run', return_value=subprocess.CompletedProcess([], 1)) as invoke, \
                    patch('episode_runtime.execute', side_effect=self.execute_grader):
                with self.assertRaises(subprocess.CalledProcessError):
                    grading(['frozen-report'], root, Mock(), threading.Event())
            self.assertEqual(invoke.call_count, 1)
            self.assertEqual(infrastructure_recovery(root / 'grading')['attempts'], [])

    def test_grading_retry_cap_survives_pause_and_restart(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            retained = root / 'retained-report.json'
            save(retained, {'mode': 'pilot', 'run_id': 'retained'})
            status = {'phase': 'blocked', 'retryable': True, 'errors': [{'retryable': True}],
                      'report_id': 'retained', 'report_sha256': file_hash(retained), 'report_path': str(retained)}
            cancelled = Mock()
            cancelled.is_set.return_value = False
            cancelled.wait.return_value = True
            def report(command, **kwargs):
                save(pathlib.Path(command[-1]), status)
                return subprocess.CompletedProcess(command, 1)
            with patch('recovery.MAX_INFRASTRUCTURE_ATTEMPTS', 3), \
                    patch('recovery.subprocess.run', side_effect=report) as invoke, \
                    patch('episode_runtime.execute', side_effect=self.execute_grader):
                self.assertFalse(grading(['frozen-report'], root, Mock(), cancelled))
                first = (root / 'grading/attempt-0000/disposition.json').read_bytes()
                cancelled.wait.return_value = False
                for _ in range(2):
                    with self.assertRaises(GradingExhausted) as caught:
                        grading(['frozen-report'], root, Mock(), cancelled)
                    self.assertTrue(caught.exception.recovery['exhausted'])
                    self.assertEqual(len(caught.exception.recovery['attempts']), 3)
                self.assertEqual(invoke.call_count, 3)
                self.assertEqual((root / 'grading/attempt-0000/disposition.json').read_bytes(), first)
            final = json.loads((root / 'grading-status.json').read_text())
            self.assertEqual(final['phase'], 'completed_with_unmeasured')
            self.assertFalse(final['retryable'])
            self.assertEqual((root / 'preview.json').read_bytes(), retained.read_bytes())
            for field in ('report_id', 'report_sha256', 'report_path', 'errors'):
                self.assertEqual(final[field], status[field])

    def test_grading_pause_before_admission_consumes_no_attempt(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            cancelled = threading.Event()
            cancelled.set()
            with patch('episode_runtime.execute', side_effect=self.execute_grader), \
                    patch('recovery.subprocess.run', side_effect=finished_report) as invoke:
                self.assertFalse(grading(['frozen-report'], root, Mock(), cancelled))
                self.assertEqual(infrastructure_recovery(root / 'grading')['attempts'], [])
                invoke.assert_not_called()
                cancelled.clear()
                self.assertTrue(grading(['frozen-report'], root, Mock(), cancelled))
                invoke.assert_called_once()

    def test_grading_without_fresh_verdict_cannot_reuse_global_status(self):
        for kind in ('execution_unknown', 'exited'):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                save(root / 'grading-status.json', {'phase': 'blocked', 'retryable': True})
                with patch('episode_runtime.execute', return_value={'kind': kind, 'returncode': 1}) as execute:
                    with self.assertRaises(RuntimeError):
                        grading(['frozen-report'], root, Mock(), threading.Event())
                execute.assert_called_once()
                self.assertEqual(infrastructure_recovery(root / 'grading')['attempts'], [])

    def test_known_grading_interruption_waits_without_stale_bindings(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            save(root / 'grading-status.json', {'phase': 'blocked', 'retryable': True,
                                                'report_id': 'old', 'report_sha256': 'old'})
            cancelled = Mock(is_set=Mock(return_value=False))
            cancelled.wait.side_effect = [False]*9 + [True]
            def interrupt(directory, command, cwd, cancelled):
                directory.mkdir(parents=True, exist_ok=True)
                path = directory / 'exit.json'
                if not path.exists():
                    save(path, {'kind': 'interrupted', 'returncode': None})
                return json.loads(path.read_text())
            with patch('recovery.MAX_INFRASTRUCTURE_ATTEMPTS', 2), \
                    patch('episode_runtime.execute', side_effect=interrupt):
                self.assertFalse(grading(['frozen-report'], root, Mock(), cancelled))
                self.assertEqual(len(list((root / 'grading').glob('attempt-*'))), 10)
            status = json.loads((root / 'grading-status.json').read_text())
            self.assertEqual(status['phase'], 'blocked')
            self.assertEqual(status['execution']['kind'], 'interrupted')
            self.assertNotIn('report_sha256', status)
            self.assertNotIn('report_id', status)

    def test_killed_report_recovers_without_exit_code_conversion_mismatch(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            cancelled = Mock()
            cancelled.is_set.return_value = False
            cancelled.wait.return_value = False
            outcomes = iter([-9, 0])
            def report(command, **kwargs):
                code = next(outcomes)
                return finished_report(command) if code == 0 else subprocess.CompletedProcess(command, code)
            with patch('episode_runtime.execute', side_effect=self.execute_grader), \
                    patch('recovery.subprocess.run', side_effect=report) as invoke:
                self.assertTrue(grading(['frozen-report'], root, Mock(), cancelled))
            self.assertEqual(invoke.call_count, 2)
            first = json.loads((root / 'grading/attempt-0000/verdict.json').read_text())
            self.assertEqual(first['report_returncode'], -9)
            self.assertEqual(first['returncode'], 1)
            self.assertEqual(len(infrastructure_recovery(root / 'grading')['attempts']), 1)

    def test_grading_supervisor_loss_recovers_without_overlapping_reports(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            script = root / 'report.py'
            script.write_text("""import fcntl, hashlib, json, pathlib, sys, time
root = pathlib.Path(__file__).parent
with (root / 'report.lock').open('a') as lock:
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        (root / 'overlap').touch()
        raise
    with (root / 'calls').open('a') as calls:
        calls.write('called\\n')
    if len((root / 'calls').read_text().splitlines()) == 1:
        while not (root / 'release').exists():
            time.sleep(.02)
    status = pathlib.Path(sys.argv[-1])
    report = status.with_name('report.json')
    report.write_text(json.dumps({'mode': 'exploration', 'run_id': 'graded'}))
    status.write_text(json.dumps({'phase': 'completed', 'retryable': False, 'report_id': 'graded',
        'report_path': str(report.resolve()), 'report_sha256': hashlib.sha256(report.read_bytes()).hexdigest()}))
""")
            command = [sys.executable, str(script)]
            driver = ('from recovery import grading; from unittest.mock import Mock; '
                      'import pathlib, threading, json, sys; '
                      'assert grading(json.loads(sys.argv[2]), pathlib.Path(sys.argv[1]), Mock(), threading.Event())')
            processes = []
            def await_path(path):
                deadline = time.monotonic() + 15
                while not path.exists():
                    if time.monotonic() > deadline:
                        self.fail('timed out awaiting ' + path.name)
                    time.sleep(.02)
            try:
                for _ in range(2):
                    processes.append(subprocess.Popen([sys.executable, '-c', driver, str(root), json.dumps(command)],
                        cwd=pathlib.Path(__file__).parent, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
                await_path(root / 'calls')
                attempt = root / 'grading/attempt-0000'
                marker = json.loads((attempt / 'launched.json').read_text())
                os.kill(marker['worker_pid'], signal.SIGKILL)
                for process in processes:
                    self.assertEqual(process.wait(timeout=20), 0)
                self.assertFalse((root / 'overlap').exists())
                self.assertEqual((root / 'calls').read_text().splitlines(), ['called', 'called'])
                self.assertEqual(json.loads((attempt / 'exit.json').read_text())['kind'], 'interrupted')
                self.assertEqual(json.loads((root / 'grading-status.json').read_text())['phase'], 'completed')
            finally:
                (root / 'release').touch()
                for process in processes:
                    if process.poll() is None:
                        process.kill()
                    process.wait(timeout=5)
                for attempt in (root / 'grading').glob('attempt-*'):
                    if (attempt / 'launched.json').exists() and not (attempt / 'exit.json').exists():
                        await_path(attempt / 'exit.json')

    def test_grading_unknown_cleanup_fences_restart(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            attempt = root / 'grading/attempt-0000'
            attempt.mkdir(parents=True)
            save(attempt / 'exit.json', {'kind': 'execution_unknown', 'returncode': None})
            with self.assertRaisesRegex(RuntimeError, 'grading/attempt-0000'):
                assert_execution_quiescence(root)

    def test_grading_admission_rejects_changed_command(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            cancelled = threading.Event()
            cancelled.set()
            with patch('episode_runtime.execute', side_effect=self.execute_grader) as execute:
                self.assertFalse(grading(['frozen-report'], root, Mock(), cancelled))
                with self.assertRaisesRegex(ValueError, 'retained admission'):
                    grading(['other-report'], root, Mock(), cancelled)
                execute.assert_called_once()

    def test_corrupt_index_rebuilds_from_finished_receipts(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            item = {'index': 0, 'model': 'candidate'}
            plan = {'episodes': [item], 'execution': {}}
            progress = Progress(root, plan)
            progress.started(item)
            progress.finished(item, {'report': 'retained'})
            (root / 'results.json').write_text('{incomplete')
            recovered = Progress(root, plan)
            self.assertEqual(recovered.pending(), [])
            self.assertEqual(recovered.records, {'0': {'report': 'retained'}})

    def test_preparation_retains_inputs_across_build_failures(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            roster = root / 'roster.json'
            roster.write_text(json.dumps({'repetitions': 25, 'models': [{'id': 'a'}, {'id': 'b'}]}))
            args = SimpleNamespace(out=root, roster=roster, repetitions=1, models='a', cases='repair', cadence='selected',
                                   concurrency=3, cloud_concurrency=1, provider_limit=['p=2'])
            first = preparation.inputs(args, 'implementation')
            args.models, args.repetitions, args.cases = None, None, None
            args.concurrency, args.cloud_concurrency, args.provider_limit = None, None, None
            self.assertEqual(first['concurrency'], 3)
            self.assertEqual(first['provider_limit'], ['p=2'])
            self.assertEqual(preparation.inputs(args, 'implementation'), first)
            with self.assertRaises(ValueError): preparation.inputs(args, 'changed runner')

    def test_lost_execution_source_is_never_replaced_by_current_source(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            (root / 'plan.json').write_text('{}')
            with patch.object(preparation, 'freeze') as freeze:
                with self.assertRaises(ValueError): preparation.source_snapshot(root)
            freeze.assert_not_called()


if __name__ == '__main__':
    unittest.main()

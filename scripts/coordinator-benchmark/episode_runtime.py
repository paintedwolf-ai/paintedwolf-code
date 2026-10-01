"""A detached, leased execution survives loss of the matrix controller."""
import argparse
import fcntl
import json
import os
import pathlib
import select
import subprocess
import sys
import threading
import time
import traceback
from progress import save, timestamp
from process_group import stop_process
from recovery import infrastructure_recovery, recovery_delay


class RetainedProcessingExhausted(RuntimeError):
    def __init__(self, recovery):
        super().__init__('retained processing exhausted its infrastructure attempts')
        self.recovery = recovery


def retained_retry(directory, attempt, result):
    if result['kind'] not in {'exited', 'interrupted', 'worker_failed'}:
        raise RuntimeError('retained processing cleanup is unconfirmed')
    path = attempt / 'disposition.json'
    if not path.exists():
        number = len(list(directory.glob('attempt-*'))) - 1
        interrupted = result['kind'] == 'interrupted'
        save(path, {'action':'retry', 'failure':{
            'kind':'harness', 'code':'retained_processing_interrupted' if interrupted else 'retained_processing_failed', 'retryable':True},
            'exit':result, 'retry_at':time.time() + recovery_delay(number)})
    recovery = infrastructure_recovery(directory)
    if recovery['exhausted']:
        raise RetainedProcessingExhausted(recovery)
    return json.loads(path.read_text())['retry_at']


def run_retained_step(directory, command, cwd, cancelled):
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (directory / 'lease').open('a') as lease:
        while True:
            try:
                fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                if cancelled.wait(.25):
                    raise InterruptedError('retained processing paused')
        attempts = sorted(directory.glob('attempt-*'))
        number = len(attempts) - 1 if attempts else 0
        while True:
            attempt = directory / f'attempt-{number:04}'
            path = attempt / 'exit.json'
            if path.exists():
                prior = json.loads(path.read_text())
                if prior['kind'] == 'exited' and prior['returncode'] == 0:
                    execute(attempt, command, cwd, cancelled)
                    return
                retry_at = retained_retry(directory, attempt, prior)
                if cancelled.is_set() or cancelled.wait(max(0, retry_at - time.time())):
                    raise InterruptedError('retained processing paused')
                number += 1
                attempt = directory / f'attempt-{number:04}'
            result = execute(attempt, command, cwd, cancelled)
            if result['kind'] == 'exited' and result['returncode'] == 0:
                return
            retained_retry(directory, attempt, result)


def lease_available(directory):
    with (directory / 'lease').open('a') as lease:
        try:
            fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return True
        except BlockingIOError:
            return False


def worker(directory):
    with (directory / 'lease').open('a') as lease:
        try:
            fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return
        if (directory / 'exit.json').exists():
            return
        marker = directory / 'launched.json'
        if marker.exists():
            # A released lease without an exit receipt does not prove cleanup.
            save(directory / 'exit.json', {'kind': 'execution_unknown', 'returncode': None})
            return
        save(marker, {'started_at': timestamp(), 'worker_pid': os.getpid()})
        read_fd, write_fd = os.pipe()
        try:
            with (directory / 'worker.log').open('a') as log:
                guard = subprocess.Popen([sys.executable, str(pathlib.Path(__file__).resolve()),
                    '--worker', str(directory), '--guard-fd', str(read_fd), '--lease-fd', str(lease.fileno())],
                    stdout=log, stderr=subprocess.STDOUT, pass_fds=(read_fd, lease.fileno()))
                os.close(read_fd)
                read_fd = None
                guard.wait()
            if not (directory / 'exit.json').exists():
                save(directory / 'exit.json', {'kind': 'execution_unknown', 'returncode': None})
        finally:
            if read_fd is not None:
                os.close(read_fd)
            os.close(write_fd)


def command_worker(directory, guard_fd):
    if select.select([guard_fd], [], [], 0)[0]:
        save(directory / 'exit.json', {'kind': 'interrupted', 'returncode': None})
        return
    spec = json.loads((directory / 'command.json').read_text())
    process = None
    try:
        with (directory / 'launcher.log').open('a') as log:
            process = subprocess.Popen(spec['command'], cwd=spec['cwd'], stdout=log,
                stderr=subprocess.STDOUT, start_new_session=True, close_fds=True)
            save(directory / 'process.json', {'pid': process.pid, 'started_at': timestamp()})
            last_update = 0
            interrupted = False
            while process.poll() is None:
                if select.select([guard_fd], [], [], 0)[0]:
                    interrupted = True
                    break
                now = time.monotonic()
                if now - last_update >= 5:
                    save(directory / 'execution.json', {'phase': 'running', 'updated_at': timestamp(),
                         'supervisor_pid': os.getppid(), 'guardian_pid': os.getpid(), 'launcher_pid': process.pid})
                    last_update = now
                time.sleep(0.25)
            save(directory / 'execution.json', {'phase': 'cleanup', 'updated_at': timestamp(),
                 'guardian_pid': os.getpid(), 'launcher_pid': process.pid,
                 'launcher_returncode': process.returncode})
            ended = {'kind': 'interrupted' if interrupted else 'exited', 'returncode': process.returncode}
    except Exception:
        (directory / 'worker-error.txt').write_text(traceback.format_exc())
        ended = {'kind': 'worker_failed', 'returncode': None}
    if process is not None:
        try:
            stop_process(process)
            if ended['kind'] in {'exited', 'interrupted'}:
                ended['returncode'] = process.returncode
        except Exception:
            (directory / 'cleanup-error.txt').write_text(traceback.format_exc())
            ended = {'kind': 'execution_unknown', 'returncode': process.returncode}
    save(directory / 'exit.json', ended)


def assert_execution_quiescence(root):
    attempts = list((root / 'grading').glob('attempt-*'))
    for scope in ('captures', 'provider-preflight'):
        attempts.extend((root / scope).glob('*/attempt-*'))
        attempts.extend((root / scope).glob('*/attempt-*/finalization/attempt-*'))
    for attempt in attempts:
        receipt = attempt / 'exit.json'
        unknown = (receipt.exists() and json.loads(receipt.read_text())['kind'] == 'execution_unknown')
        orphaned = ((attempt / 'launched.json').exists() and lease_available(attempt)
                    and not receipt.exists())
        if unknown or orphaned:
            raise RuntimeError('execution cleanup is unconfirmed: ' + str(attempt.relative_to(root)))


def execute(directory, command, cwd, cancelled):
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    spec = {'command': command, 'cwd': str(cwd)}
    path = directory / 'command.json'
    if path.exists():
        if json.loads(path.read_text()) != spec:
            raise ValueError('episode command differs from its retained admission')
    else:
        save(path, spec)
    process = None
    try:
        while not (directory / 'exit.json').exists():
            if cancelled.is_set() and process is None and not (directory / 'launched.json').exists():
                raise InterruptedError('execution admission paused before launch')
            if (process is None or process.poll() is not None) and lease_available(directory):
                with (directory / 'worker.log').open('a') as log:
                    process = subprocess.Popen([sys.executable, str(pathlib.Path(__file__).resolve()),
                        '--worker', str(directory)], stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
            time.sleep(0.25)
        # The exit receipt commits cleanup before lease release.
        return json.loads((directory / 'exit.json').read_text())
    finally:
        if process is not None:
            if (directory / 'exit.json').exists() or process.poll() is not None:
                process.wait()
            else:
                threading.Thread(target=process.wait, daemon=True).start()


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--worker', type=pathlib.Path, required=True)
    parser.add_argument('--guard-fd', type=int)
    parser.add_argument('--lease-fd', type=int)
    args = parser.parse_args()
    if args.guard_fd is None:
        worker(args.worker)
    else:
        with os.fdopen(args.lease_fd), os.fdopen(args.guard_fd):
            command_worker(args.worker, args.guard_fd)

"""Recover infrastructure operations and retain infrastructure-attempt histories."""
import argparse
import fcntl
import http.client
import json
import pathlib
import subprocess
import sys
import time
import urllib.error
from progress import save
from grading_state import report_lease, select_report
from snapshot import file_hash

MAX_INFRASTRUCTURE_ATTEMPTS = 8


def recovery_delay(attempt):
    return min(3600, 5 * 2**min(attempt, 10))


def availability_failure(failure):
    return failure.get('kind') == 'provider' and failure.get('code') in {
        'provider_rate_limited', 'provider_overloaded', 'provider_server_error',
        'provider_silent', 'provider_unreachable'}


def infrastructure_recovery(root, through=None):
    """Count completed infrastructure replacements, including those retained before a restart."""
    attempts = []
    for directory in sorted(root.glob('attempt-*')):
        if through is not None and directory.name > through:
            break
        path = directory / 'disposition.json'
        if not path.exists():
            continue
        receipt = json.loads(path.read_text())
        if receipt['action'] == 'retry' and (receipt.get('failure') or {}).get('kind') in {'provider', 'harness'}:
            attempts.append({'attempt': path.parent.name, 'failure': receipt['failure']})
    unresolved = [a for a in attempts if not availability_failure(a['failure'])
                  and a['failure'].get('code') not in {'retained_processing_interrupted', 'provider_probe_interrupted'}]
    return {'attempt_limit': MAX_INFRASTRUCTURE_ATTEMPTS, 'attempts': attempts,
            'exhausted': len(unresolved) >= MAX_INFRASTRUCTURE_ATTEMPTS}


def local_probe(operation, cancelled):
    attempt = 0
    while not cancelled.is_set():
        try:
            return operation()
        except urllib.error.HTTPError as error:
            if error.code not in {408, 429, 500, 502, 503, 504}:
                raise
        except (urllib.error.URLError, TimeoutError, http.client.IncompleteRead):
            pass
        if cancelled.wait(min(60, 2**min(attempt, 6))):
            break
        attempt += 1
    raise InterruptedError('local model identity check cancelled')


class GradingExhausted(RuntimeError):
    def __init__(self, recovery):
        super().__init__('grading exhausted its infrastructure attempts')
        self.recovery = recovery


def report_exit_status(path, returncode):
    try:
        status = json.loads(path.read_text())
        if returncode == 0:
            if status['phase'] not in {'completed', 'completed_with_unmeasured'} or status.get('retryable') is True:
                raise ValueError('successful report has no completed status')
            if not status.get('report_sha256'):
                raise ValueError('successful report has no retained result')
        elif status['phase'] != 'blocked':
            raise ValueError('failed report has no terminal failure status')
        if status.get('report_sha256'):
            report_path = pathlib.Path(status['report_path'])
            if not report_path.is_absolute() or report_path.is_symlink() or file_hash(report_path) != status['report_sha256']:
                raise ValueError('report receipt differs from its retained result')
            if json.loads(report_path.read_text())['run_id'] != status['report_id']:
                raise ValueError('report receipt differs from its grading revision')
        return status
    except (OSError, ValueError, KeyError, TypeError):
        return {'phase': 'blocked', 'retryable': False,
                'failure': {'kind': 'harness', 'code': 'grading_receipt_invalid', 'retryable': False}}


def grade_once(directory):
    spec = json.loads((directory.parent / 'command.json').read_text())
    path = directory / 'grading-status.json'
    save(path, {'phase': 'running', 'retryable': False})
    returncode = None
    try:
        result = subprocess.run([*spec['command'], '--status-out', str(path.resolve())], cwd=spec['cwd'])
        returncode = result.returncode
        status = report_exit_status(path, returncode)
    except OSError as error:
        status = {'phase': 'blocked', 'retryable': True, 'failure': {
            'kind': 'harness', 'code': 'grading_launch_failed', 'retryable': True, 'errno': error.errno}}
    save(path, status)
    code = int(returncode != 0 or status['phase'] == 'blocked')
    save(directory / 'verdict.json', {'returncode': code, 'report_returncode': returncode, 'status': status})
    return code


def grading(command, run, progress, cancelled):
    directory = run / 'grading'
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (directory / 'lease').open('a') as lease:
        while True:
            try:
                fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                if cancelled.wait(.25):
                    progress.write('paused', force=True)
                    return False
        path = directory / 'command.json'
        spec = {'command': command, 'cwd': str(run.resolve())}
        if path.exists():
            if json.loads(path.read_text()) != spec:
                raise ValueError('grading command differs from its retained admission')
        else:
            save(path, spec)
        try:
            return grading_attempts(directory, run, progress, cancelled)
        except InterruptedError:
            progress.write('paused', force=True)
            return False


def grading_attempts(directory, run, progress, cancelled):
    from episode_runtime import execute, retained_retry, RetainedProcessingExhausted
    attempts = sorted(directory.glob('attempt-*'))
    number = len(attempts) - 1 if attempts else 0
    while True:
        attempt = directory / f'attempt-{number:04}'
        command = [sys.executable, str(pathlib.Path(__file__).resolve()), '--grade-once', str(attempt.resolve())]
        progress.write('grading', force=True)
        result = execute(attempt, command, run.resolve(), cancelled)
        if result['kind'] == 'execution_unknown':
            raise RuntimeError('grading cleanup is unconfirmed')
        verdict = None
        if result['kind'] == 'exited':
            verdict_path = attempt / 'verdict.json'
            if not verdict_path.exists():
                raise RuntimeError('grading ended without a completed verdict')
            verdict = json.loads(verdict_path.read_text())
            if verdict['returncode'] != result['returncode']:
                raise ValueError('grading verdict differs from its execution receipt')
        report_returncode = verdict['report_returncode'] if verdict is not None else None
        if result['kind'] in {'interrupted', 'worker_failed'} or (
                report_returncode is not None and report_returncode < 0):
            status = {'phase': 'blocked', 'retryable': True,
                      'failure': {'kind': 'harness', 'code': 'grading_execution_interrupted', 'retryable': True},
                      'execution': result}
            if verdict is not None:
                status['report_returncode'] = verdict['report_returncode']
        elif verdict is not None:
            status = verdict['status']
        else:
            raise RuntimeError('grading ended without a completed verdict')
        with report_lease(run):
            select_report(run, status, publish=True)
        if verdict is not None and (report_returncode is None or report_returncode >= 0):
            if result['returncode'] == 0:
                return True
            if status.get('retryable') is not True:
                spec = json.loads((directory / 'command.json').read_text())
                raise subprocess.CalledProcessError(verdict['report_returncode'] or 1, spec['command'])
        try:
            retry_at = retained_retry(directory, attempt, result)
        except RetainedProcessingExhausted as error:
            with report_lease(run):
                select_report(run, {**status, 'phase': 'completed_with_unmeasured',
                                    'retryable': False, 'recovery': error.recovery}, publish=True)
            raise GradingExhausted(error.recovery) from error
        progress.write('waiting_for_grader', force=True)
        if cancelled.is_set() or cancelled.wait(max(0, retry_at - time.time())):
            raise InterruptedError('grading paused')
        number += 1


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--grade-once', required=True, type=pathlib.Path)
    args = parser.parse_args()
    raise SystemExit(grade_once(args.grade_once))

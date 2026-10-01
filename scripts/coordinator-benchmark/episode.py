"""Retain infrastructure attempts until one application outcome is measurable."""
import argparse
import json
import pathlib
import shutil
import time
import subprocess
import sys
import threading
from artifacts import InvalidCandidate, retain_project, retain_outside
from application_environment import application_environment, isolated_command
from capture_storage import cleanup_private_config, deduplicate_runtime
from episode_runtime import execute, lease_available, run_retained_step, RetainedProcessingExhausted
from local_runtime import verify_runtime
from progress import save, timestamp
from recovery import local_probe, infrastructure_recovery, recovery_delay
from runtime_resources import release_external_resource


def disposition(exit_record, report, setup_failure=None):
    if exit_record['kind'] == 'execution_unknown':
        return 'blocked', 'execution_unknown'
    cases = report.get('cases', []) if report else []
    case = cases[0] if len(cases) == 1 else None
    failure = case.get('failure') if case else (setup_failure or {}).get('failure')
    if case and case['status'] in {'review_required', 'failed', 'interaction_exhausted'}:
        if case['status'] == 'review_required' and failure is None:
            return 'measured', None
        if (case['status'] in {'failed', 'interaction_exhausted'} and failure
                and failure.get('kind') == 'model' and failure.get('code')
                and failure.get('retryable') is False):
            return 'measured', None
        return 'blocked', 'execution_contract_invalid'
    if failure and failure['kind'] == 'provider':
        # Replace tasks only before candidate output; the application recovers committed work.
        allowance = (case or {}).get('task_allowance')
        if (failure['retryable'] is True and failure['code'] != 'provider_empty_completion'
                and ((allowance is not None and allowance['completed'] == 0) or (case is None and setup_failure is not None))):
            return 'retry', failure['code']
        return 'blocked', failure['code']
    if failure == {'kind': 'harness', 'code': 'preparation_transport', 'retryable': True}:
        return 'retry', 'preparation_transport'
    if failure == {'kind': 'harness', 'code': 'configuration_mismatch', 'retryable': False}:
        return 'blocked', 'configuration_mismatch'
    if exit_record['kind'] == 'interrupted':
        return 'blocked', 'execution_interrupted'
    if failure and failure['kind'] == 'harness':
        return 'blocked', failure['code']
    return 'blocked', 'execution_unresolved'


def command_for(run, plan, private, item, capture):
    source = run / 'source'
    model = next(m for m in plan['models'] if m['id'] == item['model'])
    worker = plan['roster']['worker']
    return isolated_command(['bash', 'scripts/eval-agent-live.sh', '--allow-live', '--source-config', str(private / 'resolved'),
            '--provider', model['provider'], '--model', model['model'], '--worker-provider', worker['provider'],
            '--worker-model', worker['model'], '--label', f"{model['id']}-{item['case']}-{item['repeat']+1}",
            '--suite', str(source / 'lycaon/test/fixtures/eval/coordinator-benchmark.json'), '--cases', item['case'],
            '--runs', '1', '--timeout', plan['roster']['episode_timeout'], '--engine', str(source / '.bin/lycaon-dev'),
            '--driver', str(source / '.bin/lycaon-debug'), '--application', str(source / 'application.json'),
            '--rate-state-dir', str(run / 'provider-rate-state'), '--expected-plan', str(run / 'plan.json'),
            '--out-dir', str(capture)], source)


def finalize(run, capture, model, private, cancelled):
    command = [sys.executable, str(run / 'source/scripts/coordinator-benchmark/episode.py'),
               '--finalize', '--run', str(run), '--capture', str(capture),
               '--model', model['id'], '--private', str(private)]
    run_retained_step(capture.parent / 'finalization', isolated_command(command, run / 'source'),
                      run / 'source', cancelled)


def finalize_capture(run, capture, model, private):
    source = run / 'source'
    path = capture / 'report.json'
    cancelled = threading.Event()
    with (capture.parent / 'finalization.log').open('a') as log:
        subprocess.run([str(source / '.bin/lycaon-debug'), 'eval', 'tool-usage', '--refresh', str(path)],
                       cwd=source / 'lycaon', stdout=log, stderr=subprocess.STDOUT, check=True,
                       env=application_environment())
    runtime = local_probe(lambda: verify_runtime(model['configuration'], private / 'resolved'), cancelled)
    if runtime is not None:
        save(capture / 'runtime-verified.json', {'identity': runtime})
    report = json.loads(path.read_text())
    for case in report['cases']:
        if case.get('project_dir') and pathlib.Path(case['project_dir']).is_dir():
            try:
                retain_project(capture, case)
            except InvalidCandidate:
                pass  # The independent grader measures this declared artifact violation.
        if (case.get('sandbox') or {}).get('kind') == 'write_root':
            try:
                retain_outside(capture, case)
            except InvalidCandidate:
                pass
    deduplicate_runtime(capture, source)
    release_external_resource(capture, source)


def reattachment_order(run, pending):
    def leased(item):
        root = run / 'captures' / f"episode-{item['index']:03}"
        attempts = sorted(root.glob('attempt-*'))
        return bool(attempts and not lease_available(attempts[-1]))
    # Surviving executions consume provider capacity before any new admission.
    return sorted(pending, key=lambda item: not leased(item))


def exhausted_infrastructure(run, root):
    recovery = infrastructure_recovery(root)
    if not recovery['exhausted']:
        return None
    last = recovery['attempts'][-1]
    return {'blocked': 'infrastructure_recovery_exhausted',
            'capture': str((root / last['attempt'] / 'capture').relative_to(run)),
            'failure': last['failure'], 'recovery': recovery}


def episode(run, plan, private, item, cancelled):
    if cancelled.is_set():
        return None
    source = run / 'source'
    root = run / 'captures' / f"episode-{item['index']:03}"
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    model = next(m for m in plan['models'] if m['id'] == item['model'])
    attempts = sorted(root.glob('attempt-*'))
    number = len(attempts) - 1 if attempts else 0
    if attempts and (attempts[-1] / 'disposition.json').exists():
        prior = json.loads((attempts[-1] / 'disposition.json').read_text())
        if prior['action'] == 'retry':
            if exhausted := exhausted_infrastructure(run, root):
                return exhausted
            if prior['retry_at'] > time.time():
                return {'retry_at': prior['retry_at']}
            number += 1
    if exhausted := exhausted_infrastructure(run, root):
        return exhausted
    if shutil.disk_usage(run).free < 2 * 1024**3:
        return {'error': 'storage_unavailable'}
    attempt = root / f'attempt-{number:04}'
    capture = attempt / 'capture'
    if not (attempt / 'command.json').exists():
        local_probe(lambda: verify_runtime(model['configuration'], private / 'resolved'), cancelled)
    exit_record = execute(attempt, command_for(run, plan, private, item, capture), source, cancelled)
    cleanup_private_config(capture / 'config', exit_record)
    path = capture / 'report.json'
    raw = json.loads(path.read_text()) if path.exists() else None
    setup_path = capture / 'setup-failure.json'
    setup_failure = json.loads(setup_path.read_text()) if setup_path.exists() else None
    if raw is not None and exit_record['kind'] != 'execution_unknown':
        try:
            finalize(run, capture, model, private, cancelled)
        except RetainedProcessingExhausted as error:
            failure = {'kind':'harness', 'code':'finalization_recovery_exhausted', 'retryable':False}
            save(attempt / 'disposition.json', {'action':'blocked', 'cause':failure['code'],
                 'failure':failure, 'finished_at':timestamp()})
            return {'blocked':failure['code'], 'capture':str(capture.relative_to(run)),
                    'failure':failure, 'finalization_recovery':error.recovery}
        raw = json.loads(path.read_text())
    action, cause = disposition(exit_record, raw, setup_failure)
    failure = raw['cases'][0].get('failure') if raw and len(raw.get('cases', [])) == 1 else (setup_failure or {}).get('failure')
    receipt = {'action': action, 'cause': cause, 'failure': failure, 'finished_at': timestamp()}
    if action == 'retry':
        receipt['retry_at'] = time.time() + recovery_delay(number)
    save(attempt / 'disposition.json', receipt)
    if action == 'measured':
        return {'report': str(path), 'capture': str(capture.relative_to(run)),
                'infrastructure_attempts': number}
    if action == 'blocked':
        return {'blocked': cause, 'capture': str(capture.relative_to(run)),
                'failure': failure}
    if exhausted := exhausted_infrastructure(run, root):
        return exhausted
    return {'retry_at': receipt['retry_at']}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--finalize', action='store_true', required=True)
    parser.add_argument('--run', type=pathlib.Path, required=True)
    parser.add_argument('--capture', type=pathlib.Path, required=True)
    parser.add_argument('--model', required=True)
    parser.add_argument('--private', type=pathlib.Path, required=True)
    args = parser.parse_args()
    plan = json.loads((args.run / 'plan.json').read_text())
    model = next(model for model in plan['models'] if model['id'] == args.model)
    finalize_capture(args.run, args.capture, model, args.private)

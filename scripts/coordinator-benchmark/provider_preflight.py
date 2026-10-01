"""Check application/provider conversation compatibility before matrix admission."""
import argparse
import json
import pathlib
import shutil
import sys
import time
import threading
from episode_runtime import execute, assert_execution_quiescence
from capture_storage import cleanup_private_config
from application_environment import isolated_command
from progress import save, timestamp
from snapshot import identity
from smoke import Application
from recovery import infrastructure_recovery, recovery_delay

STAGES = ['imported_tool_results', 'host_continuation']


def validate_receipt(receipt, model):
    if (receipt['provider'], receipt['model']) != (model['provider'], model['model']):
        raise ValueError('provider preflight returned another model identity')
    if type(receipt['accepted']) is not bool:
        raise ValueError('provider preflight acceptance must be boolean')
    role = model.get('role', 'coordinator')
    if receipt.get('role', 'coordinator') != role:
        raise ValueError('provider preflight returned another application role')
    expected = ['utility_text'] if role == 'lite' else STAGES
    if receipt['accepted'] is True:
        if ([stage['stage'] for stage in receipt['stages']] != expected
                or not all(stage['accepted'] is True for stage in receipt['stages'])):
            raise ValueError('provider preflight did not accept every conversation shape')
    elif type(receipt.get('retryable')) is not bool or not receipt.get('code'):
        raise ValueError('provider preflight failure lacks a structured disposition')
    return receipt


def probe(run, private, model, cancelled):
    binding = identity(model['configuration'])
    root = run/'provider-preflight'/model['id']
    root.mkdir(parents=True, exist_ok=True)
    final = root/'result.json'
    if final.exists():
        receipt = json.loads(final.read_text())
        if receipt['configuration_sha256'] != binding:
            raise ValueError('provider preflight configuration changed')
        return validate_receipt(receipt, model)
    attempts = sorted(root.glob('attempt-*'))
    number = len(attempts)-1 if attempts else 0
    while not cancelled.is_set():
        attempt = root/f'attempt-{number:04}'
        disposition = attempt / 'disposition.json'
        if disposition.exists():
            retry = json.loads(disposition.read_text())
            if cancelled.wait(max(0, retry['retry_at'] - time.time())):
                break
            number += 1
            continue
        command = [sys.executable, str(run/'source/scripts/coordinator-benchmark/provider_preflight.py'),
                   '--allow-live', '--source-config', str(private/'resolved'), '--out', str(attempt/'probe'),
                   '--provider', model['provider'], '--model', model['model'], '--role', model.get('role','coordinator'),
                   '--rate-state', str(run/'provider-rate-state')]
        exit_record = execute(attempt, isolated_command(command, run/'source'), run/'source', cancelled)
        cleanup_private_config(attempt / 'probe/application/config', exit_record)
        result = attempt/'probe/result.json'
        unknown = exit_record['kind'] == 'execution_unknown'
        if result.exists() and not unknown:
            receipt = validate_receipt(json.loads(result.read_text()), model)
            if receipt['accepted'] or not receipt['retryable']:
                save(final, {**receipt, 'configuration_sha256':binding, 'completed_at':timestamp()})
                return receipt
        elif exit_record['kind'] == 'interrupted':
            receipt = {'provider':model['provider'], 'model':model['model'], 'role':model.get('role','coordinator'),
                       'accepted':False, 'stages':[], 'retryable':True,
                       'code':'provider_probe_interrupted', 'failure_kind':'harness'}
        else:
            receipt = {'provider':model['provider'], 'model':model['model'], 'role':model.get('role','coordinator'), 'accepted':False,
                       'stages':[], 'retryable':False,
                       'code':'provider_probe_execution_unknown' if unknown else 'provider_probe_harness_failed',
                       'failure_kind':'harness', 'attempt':str(attempt.relative_to(run))}
            save(final, {**receipt, 'configuration_sha256':binding, 'completed_at':timestamp()})
            return receipt
        save(attempt/'disposition.json', {'action':'retry', 'retry_at':time.time() + recovery_delay(number), 'failure':{
            'kind':receipt.get('failure_kind','provider'), 'code':receipt['code'], 'retryable':receipt['retryable']}})
        recovery = infrastructure_recovery(root)
        if recovery['exhausted']:
            exhausted = {**receipt, 'retryable':False, 'code':'infrastructure_recovery_exhausted',
                         'recovery':recovery, 'last_failure':receipt}
            save(final, {**exhausted, 'configuration_sha256':binding, 'completed_at':timestamp()})
            return exhausted
    raise InterruptedError('provider preflight paused')


def dependencies(model):
    config = model['configuration']
    refs = [{**config['coordinator'], 'role':'coordinator'},
            *[{**ref,'role':'agent_pool'} for ref in config.get('workers', [])]]
    if config.get('lite'):
        refs.append({**config['lite'],'role':'lite'})
    result = {}
    for ref in refs:
        result.setdefault((ref['provider'],ref['model'],ref['role']),ref)
    return result


class Admission:
    def __init__(self, run, private, plan, cancelled):
        self.run, self.private, self.cancelled = run, private, cancelled
        self.models = {model['id']: model for model in plan['models']}
        self.probes = {(model['provider'], model['model'], 'coordinator'):model for model in plan['models']}
        for model in plan['models']:
            for key, ref in dependencies(model).items():
                if key in self.probes:
                    continue
                configuration = {name:value for name,value in model['configuration'].items()
                                 if name != 'configuration_sha256'}
                configuration['coordinator'] = ref
                configuration['configuration_sha256'] = identity(configuration)
                self.probes[key] = {'id':'support-'+identity(ref)[:16], 'provider':ref['provider'], 'model':ref['model'],
                                   'role':ref['role'], 'configuration':configuration}
        self.locks = {key:threading.Lock() for key in self.probes}
        self.receipts = {}

    def check(self, model_id):
        failures = []
        for key in dependencies(self.models[model_id]):
            with self.locks[key]:
                if key not in self.receipts:
                    assert_execution_quiescence(self.run)
                    self.receipts[key] = probe(self.run, self.private, self.probes[key], self.cancelled)
                    assert_execution_quiescence(self.run)
                receipt = self.receipts[key]
            if not receipt['accepted']:
                failures.append(receipt)
        if not failures:
            return None
        receipt = failures[0]
        return {'blocked':'provider_preflight', 'failure':{
            'kind':receipt.get('failure_kind','provider'), 'code':receipt['code'], 'retryable':False},
            'preflight_failures':failures}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--allow-live', action='store_true')
    parser.add_argument('--source-config', type=pathlib.Path, required=True)
    parser.add_argument('--out', type=pathlib.Path, required=True)
    parser.add_argument('--rate-state', type=pathlib.Path, required=True)
    parser.add_argument('--provider', required=True)
    parser.add_argument('--model', required=True)
    parser.add_argument('--role', choices=['coordinator','agent_pool','lite'], default='coordinator')
    args = parser.parse_args()
    if not args.allow_live:
        raise ValueError('provider preflight spends tokens and requires --allow-live')
    args.out.mkdir(parents=True,exist_ok=True)
    application = Application(args.out/'application', args.source_config, args.rate_state)
    try:
        application.ready()
        receipt = application.request('/harness/llm/provider-probe', {
            'allow_live':True,'provider':args.provider,'model':args.model,'role':args.role})
        validate_receipt(receipt, {'provider':args.provider,'model':args.model,'role':args.role})
        save(args.out/'result.json', receipt)
    finally:
        application.close()
        shutil.rmtree(application.config)


if __name__ == '__main__':
    main()

"""Recover only the loopback endpoint from an exact retained physical fixture registration."""
import hashlib
import json
import os
from pathlib import Path
import re
import stat

from .package_payload import stable
from ..installed_input import protocol
from .provider import gui_endpoint
from .records import expand_physical_transactions


def load(binding):
    inputs = binding.get('case_inputs', {}).get('desktop', {})
    if inputs in ({}, {'format_version': 1}):
        return None
    if set(inputs) != {'format_version', 'provider_recovery'} or type(inputs['format_version']) is not int or inputs['format_version'] != 1:
        raise ValueError('provider recovery requires its declared desktop input')
    selected = inputs['provider_recovery']
    if (not isinstance(selected, dict) or set(selected) != {'prior_execution', 'sha256'}
            or not isinstance(selected['prior_execution'], str) or not isinstance(selected['sha256'], str) or not re.fullmatch('[a-f0-9]{64}', selected['sha256'])):
        raise ValueError('provider recovery requires an exact retained execution digest')
    path = Path(selected['prior_execution'])
    if not path.is_absolute() or path.resolve(strict=True) != path:
        raise ValueError('provider recovery requires a physical retained execution')
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, 'rb') as stream:
        before = os.fstat(stream.fileno())
        if not stat.S_ISREG(before.st_mode) or before.st_uid != os.getuid() or before.st_mode & 0o022 or not 0 < before.st_size <= 1 << 20:
            raise ValueError('provider recovery retained execution exceeds its owned bound')
        raw = stream.read((1 << 20) + 1)
        if stable(before) != stable(os.fstat(stream.fileno())) or stable(before) != stable(path.lstat()):
            raise ValueError('provider recovery execution changed during observation')
    if hashlib.sha256(raw).hexdigest() != selected['sha256']:
        raise ValueError('provider recovery execution digest differs')
    witness = registered(json.loads(raw), binding, str(os.getuid()), path)
    return {**witness, 'prior_execution_sha256': selected['sha256']}, raw


def registered(document, binding, account, leaf=None):
    if not isinstance(account, str) or not account.isdecimal() or int(account) <= 0:
        raise ValueError('provider recovery requires a real owned graphical UID')
    if (not isinstance(document, dict) or type(document.get('format_version')) is not int or document['format_version'] != 1
            or document.get('source_commit') != binding['source_commit'] or document.get('artifact') != binding['artifact']
            or document.get('capability') != 'conversation' or document.get('case') != 'restart-and-reconnect'
            or document.get('status') not in {'unverified', 'failed', 'passed'}):
        raise ValueError('provider recovery differs from its selected artifact and case')
    observations = document.get('observations')
    if not isinstance(observations, dict) or not isinstance(observations.get('launch_before'), dict):
        raise ValueError('provider recovery requires its prior captured launch')
    launch = observations['launch_before']
    if not isinstance(launch.get('session'), dict) or launch['session'].get('User') != account:
        raise ValueError('provider recovery requires the same owned graphical account')
    records = launch.get('fixed_input', [])
    if not isinstance(records, list) or len(records) > 4:
        raise ValueError('provider recovery registration exceeds its transaction bound')
    records = expand_physical_transactions(leaf, records)
    matches = [row for row in records if isinstance(row, dict) and isinstance(row.get('operation'), str)
               and re.fullmatch('provider-endpoint-v1-[0-9]{4,5}', row['operation'])]
    if len(matches) != 1:
        raise ValueError('provider recovery has no unique completed endpoint transaction')
    record = matches[0]
    operation = record['operation']
    sequence = protocol.steps(operation)
    events = record.get('events')
    native = binding['native_input']
    if (set(record) != {'format_version', 'vm_id', 'transaction_id', 'operation', 'completed', 'events', 'text_readback'}
            or record.get('text_readback') is not False or type(record.get('format_version')) is not int or record['format_version'] != 1
            or record.get('completed') is not True or record.get('vm_id') != native['vm_id']
            or not isinstance(events, list) or len(events) != len(sequence)):
        raise ValueError('provider recovery endpoint was not physically completed in this owned VM')
    execution = protocol.binding(events[0]['request']['binding'])
    if (execution['source_commit'] != binding['source_commit'] or execution['artifact_id'] != binding['artifact']['id']
            or execution['execution_nonce'] == native['binding']['execution_nonce']
            or any(execution[name] != native['binding'][name] for name in ('environment_id', 'variant_id'))):
        raise ValueError('provider recovery requires a distinct prior execution of these bytes')
    for ordinal, event in enumerate(events):
        if not isinstance(event, dict) or set(event) != {'request', 'delivery', 'focus_after'}:
            raise ValueError('provider recovery physical event has an unknown shape')
        pending = protocol.request(event['request'], execution)
        acknowledgment = {'format_version': 1, 'request_sha256': protocol.request_sha256(pending),
                          'delivery': event['delivery']}
        protocol.acknowledgment(acknowledgment, pending, native['vm_id'])
        if (pending['operation'] != operation or pending['ordinal'] != ordinal
                or pending['transaction_id'] != record['transaction_id'] or event['focus_after'] != pending['focus']):
            raise ValueError('provider recovery physical transaction or focus changed')
    return {'endpoint': gui_endpoint(int(operation.rsplit('-', 1)[1])),
            'prior_source_commit': execution['source_commit'], 'prior_driver_tree': execution['driver_tree'],
            'prior_execution_nonce': execution['execution_nonce'], 'vm_id': native['vm_id']}


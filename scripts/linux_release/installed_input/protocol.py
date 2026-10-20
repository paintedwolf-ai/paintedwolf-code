"""Fixed native input operations and the fresh controller/guest step wire contract."""
from __future__ import annotations

import hashlib
import json
import re

from ..installed_evidence import IDENTIFIER

EDITOR_TEXT = 'Installed editor fixture changed through the native UI.\nFind the qualification needle.\n'
MAX_TRANSACTIONS = 128
MAX_DOCUMENT = 16 << 10
_BINDING = {'format_version', 'execution_nonce', 'source_commit', 'driver_tree',
            'artifact_id', 'environment_id', 'variant_id'}


def _editor_steps() -> tuple[tuple[str, ...], ...]:
    punctuation = {' ': 'spc', '.': 'dot', '\n': 'ret'}
    rows = [('ctrl', 'a')]
    for char in EDITOR_TEXT:
        if 'A' <= char <= 'Z':
            rows.append(('shift', char.lower()))
        elif 'a' <= char <= 'z':
            rows.append((char,))
        else:
            rows.append((punctuation[char],))
    return tuple([*rows, ('ctrl', 's')])


_EDITOR_STEPS = _editor_steps()
_SEARCH_STEPS = (('ctrl', 'a'), *(('spc',) if char == ' ' else (char,)
                  for char in 'qualification needle'), ('ret',))


def steps(operation: str) -> tuple[tuple[str, ...], ...]:
    if not isinstance(operation, str):
        raise ValueError('native input requires a fixed declared operation')
    if operation == 'editor-replace-save-v1':
        return _EDITOR_STEPS
    if operation == 'editor-find-needle-v1':
        return _SEARCH_STEPS
    raise ValueError('native input requires a fixed declared operation')


def event_sha256(operation: str, ordinal: int) -> str:
    events = steps(operation)
    if type(ordinal) is not int or not 0 <= ordinal < len(events):
        raise ValueError('native input ordinal differs from its fixed operation')
    return hashlib.sha256(json.dumps(list(events[ordinal]), separators=(',', ':')).encode()).hexdigest()


def nonce(value: str) -> str:
    if not isinstance(value, str) or not re.fullmatch(r'[a-f0-9]{32}', value):
        raise ValueError('native input requires a fresh bounded nonce')
    return value


def canonical(document: dict) -> bytes:
    raw = (json.dumps(document, sort_keys=True, separators=(',', ':'), ensure_ascii=True, allow_nan=False) + '\n').encode()
    if len(raw) > MAX_DOCUMENT:
        raise ValueError('native input document exceeds its bound')
    return raw


def binding(value: dict) -> dict:
    if (not isinstance(value, dict) or set(value) != _BINDING
            or type(value['format_version']) is not int or value['format_version'] != 1):
        raise ValueError('native input requires the exact execution binding')
    nonce(value['execution_nonce'])
    for name in ('source_commit', 'driver_tree'):
        if not isinstance(value[name], str) or not re.fullmatch(r'[a-f0-9]{40}', value[name]):
            raise ValueError('native input source binding is invalid')
    for name in ('artifact_id', 'environment_id', 'variant_id'):
        if not isinstance(value[name], str) or not IDENTIFIER.fullmatch(value[name]):
            raise ValueError('native input artifact/environment/variant binding is invalid')
    return value


def process_identity(value: dict) -> dict:
    if (not isinstance(value, dict) or set(value) != {'pid', 'parent_pid', 'start_time_ticks'}
            or any(type(number) is not int for number in value.values())
            or value['pid'] <= 1 or value['parent_pid'] < 1 or value['start_time_ticks'] <= 0):
        raise ValueError('native input process identity is invalid')
    return value


def focus(value: dict) -> dict:
    fields = {'launch', 'entry', 'frame', 'entry_focused', 'frame_active', 'frame_visible'}
    if not isinstance(value, dict) or set(value) != fields:
        raise ValueError('native input focus proof has an unknown shape')
    for name in ('launch', 'entry', 'frame'):
        process_identity(value[name])
    if value['frame'] != value['launch'] or any(value[name] is not True
            for name in ('entry_focused', 'frame_active', 'frame_visible')):
        raise ValueError('native input requires its focused entry and active captured frame')
    return value


def request(document: dict, execution: dict) -> dict:
    binding(execution)
    fields = {'format_version', 'binding', 'transaction_id', 'operation', 'ordinal', 'focus'}
    if (not isinstance(document, dict) or set(document) != fields
            or type(document['format_version']) is not int or document['format_version'] != 1
            or document['binding'] != execution):
        raise ValueError('native input request differs from its fresh execution')
    binding(document['binding'])
    nonce(document['transaction_id'])
    event_sha256(document['operation'], document['ordinal'])
    focus(document['focus'])
    canonical(document)
    return document


def request_sha256(document: dict) -> str:
    return hashlib.sha256(canonical(document)).hexdigest()


def acknowledgment(document: dict, pending: dict, vm_id: str) -> dict:
    if (not isinstance(document, dict) or set(document) != {'format_version', 'request_sha256', 'delivery'}
            or type(document['format_version']) is not int or document['format_version'] != 1
            or document['request_sha256'] != request_sha256(pending)):
        raise ValueError('native input acknowledgment is stale or differs from its pending step')
    row = document['delivery']
    fields = {'format_version', 'execution_nonce', 'vm_id', 'transaction_id', 'operation',
              'ordinal', 'event_sha256', 'lease', 'qmp_ack'}
    if not isinstance(row, dict) or set(row) != fields or type(row['format_version']) is not int or row['format_version'] != 1:
        raise ValueError('native input delivery has an unknown shape')
    expected = {'execution_nonce': pending['binding']['execution_nonce'], 'vm_id': vm_id,
                'transaction_id': pending['transaction_id'], 'operation': pending['operation'],
                'ordinal': pending['ordinal'], 'event_sha256': event_sha256(pending['operation'], pending['ordinal'])}
    if any(type(row[name]) is not type(value) or row[name] != value for name, value in expected.items()):
        raise ValueError('native input delivery differs from its selected step')
    ack = row['qmp_ack']
    if (not isinstance(ack, dict) or set(ack) != {'id', 'return'} or ack['return'] != {}
            or type(ack['id']) is not int or not 0 < ack['id'] < 1 << 31
            or not isinstance(row['lease'], dict) or row['lease'].get('vm_id') != vm_id):
        raise ValueError('native input delivery omitted its actual bounded QMP acknowledgment')
    canonical(document)
    return row

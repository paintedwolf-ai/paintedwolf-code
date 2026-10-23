"""The fixed command-schema request uses the host's declared tool-result grammar."""
from __future__ import annotations

import json
import re
import threading
import uuid

from ..installed_evidence import IDENTIFIER

LOAD_ID = 'qualification-load-command'
REFUSALS = frozenset({'acquisition-surface-invalid', 'acquisition-result-missing',
                      'acquisition-result-invalid', 'acquisition-command-not-offered'})


class AcquisitionRefusal(ValueError):
    def __init__(self, code):
        if code not in REFUSALS:
            raise ValueError('acquisition refusal code is not declared')
        self.code = code
        super().__init__(code)


def object_json(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError('duplicate structured acquisition field')
            result[key] = value
        return result
    if not isinstance(raw, str) or not 0 < len(raw.encode()) <= 32768:
        raise ValueError('structured acquisition object exceeds its bound')
    def constant(value):
        raise ValueError('non-JSON acquisition constant')
    result = json.loads(raw, object_pairs_hook=pairs, parse_constant=constant)
    if not isinstance(result, dict):
        raise ValueError('structured acquisition requires an object')
    return result


def loaded_command(payload):
    if (set(payload) - {'need', 'loaded', 'already_loaded', 'nearest', 'note', 'discovery'}
            or payload.get('need') != 'command' or 'loaded' not in payload):
        raise ValueError('acquisition result differs from the fixed command request')
    names = []
    for key in ('loaded', 'already_loaded'):
        values = payload.get(key)
        # Go's required loaded slice is null when the tool was already loaded.
        if values is None:
            values = []
        if (not isinstance(values, list) or len(values) > 16
                or any(not isinstance(value, str) or not 0 < len(value) <= 128
                       or not IDENTIFIER.fullmatch(value) for value in values)):
            raise ValueError('acquisition loaded identifiers exceed their bound')
        names.extend(values)
    if 'command' not in names:
        raise ValueError('acquisition did not load the fixed command schema')
    return True


def host_call_id(value):
    if not isinstance(value, str) or not re.fullmatch(r'call_[0-9a-f]{8}', value):
        raise ValueError('fixture requires the declared canonical host call identity')
    return value


def projected_receipt(value, tool, call_id):
    if (not isinstance(value, dict) or value.get('tool') != tool
            or value.get('tool_call_id') != call_id or value.get('status') != 'completed'
            or value.get('invoked') is not True or value.get('code', '') != ''):
        raise ValueError('acquisition has no matching successful invoked receipt')
    identifier = value.get('id')
    if not isinstance(identifier, str) or str(uuid.UUID(identifier)) != identifier:
        raise ValueError('acquisition receipt requires a canonical durable UUID')
    return identifier


def result_payload(content, tool, call_id):
    host_call_id(call_id)
    if not isinstance(content, str) or not 0 < len(content.encode()) <= 32768:
        raise ValueError('projected acquisition result exceeds its bound')
    parts = content.split('\n\n')
    if len(parts) != 2:
        raise ValueError('acquisition result has an unknown projection grammar')
    decoded = []
    for part, marker in zip(parts, ('⟦D:tool⟧', '⟦D:host:tool_feedback⟧')):
        lines = part.split('\n')
        if len(lines) != 2 or lines[0] != marker or not lines[1].startswith('⟦D⟧'):
            raise ValueError('acquisition result has an unknown host-written marker')
        decoded.append(object_json(lines[1][len('⟦D⟧'):]))
    payload, feedback = decoded
    if tool == 'request_tools':
        loaded_command(payload)
    if set(feedback) != {'invocation'}:
        raise ValueError('acquisition receipt projection is missing or truncated')
    return {'tool_call_id': call_id, 'invocation_id': projected_receipt(feedback['invocation'], tool, call_id),
            **({'need': 'command', 'loaded': True} if tool == 'request_tools' else {})}


def paired_result(messages, tool, arguments, expected=None):
    last_user = max((i for i, row in enumerate(messages) if row['role'] == 'user'), default=-1)
    history = messages[last_user + 1:]
    calls = [call for row in history if row['role'] == 'assistant'
             for call in row.get('tool_calls', []) if isinstance(call, dict)
             and call.get('function', {}).get('name') == tool]
    if len(calls) != 1:
        raise AcquisitionRefusal('acquisition-result-missing')
    call = calls[0]
    try:
        identifier = host_call_id(call.get('id'))
        if (call.get('type') != 'function' or object_json(call['function']['arguments']) != arguments
                or expected is not None and identifier != expected):
            raise ValueError('fixture originating call differs from its declared phase')
        results = [row for row in history if row['role'] == 'tool'
                   and row.get('tool_call_id') == identifier]
        if len(results) != 1:
            raise ValueError('fixture call has no unique paired result')
        return result_payload(results[0].get('content'), tool, identifier)
    except (ValueError, TypeError, KeyError, AttributeError):
        raise AcquisitionRefusal('acquisition-result-invalid') from None


class CommandAcquisition:
    """Fixed emissions bind their subsequent host-issued call/result identities."""
    def __init__(self):
        self.lock = threading.Lock()
        self.issued = self.command_issued = self.completed = False
        self.witness = self.command_witness = None

    def advance(self, messages, available):
        with self.lock:
            if self.completed:
                raise AcquisitionRefusal('acquisition-result-invalid')
            if not self.issued:
                if ('command' in available or 'request_tools' not in available
                        or any(row['role'] in {'assistant', 'tool'} for row in messages)):
                    raise AcquisitionRefusal('acquisition-surface-invalid')
                self.issued = True
                return {'id': LOAD_ID, 'type': 'function', 'function': {
                    'name': 'request_tools', 'arguments': json.dumps({'need': 'command'})}}
            witness = paired_result(messages, 'request_tools', {'need': 'command'},
                                    self.witness['tool_call_id'] if self.witness else None)
            if 'command' not in available:
                raise AcquisitionRefusal('acquisition-command-not-offered')
            if self.witness is not None and self.witness != witness:
                raise AcquisitionRefusal('acquisition-result-invalid')
            self.witness = witness
            if self.command_issued:
                self.command_witness = paired_result(messages, 'command', {'command': '/usr/bin/true'})
                if self.command_witness['tool_call_id'] == witness['tool_call_id']:
                    raise AcquisitionRefusal('acquisition-result-invalid')
                self.completed = True
            else:
                self.command_issued = True
            return None

"""The fixed command-schema request uses the host's declared tool-result grammar."""
from __future__ import annotations

import json
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


def projected_receipt(value):
    if (not isinstance(value, dict) or value.get('tool') != 'request_tools'
            or value.get('tool_call_id') != LOAD_ID or value.get('status') != 'completed'
            or value.get('invoked') is not True or value.get('code', '') != ''):
        raise ValueError('acquisition has no matching successful invoked receipt')
    identifier = value.get('id')
    if not isinstance(identifier, str) or str(uuid.UUID(identifier)) != identifier:
        raise ValueError('acquisition receipt requires a canonical durable UUID')
    return identifier


def result_payload(content):
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
    loaded_command(payload)
    if set(feedback) != {'invocation'}:
        raise ValueError('acquisition receipt projection is missing or truncated')
    return {'need': 'command', 'loaded': True, 'invocation_id': projected_receipt(feedback['invocation'])}


class CommandAcquisition:
    """Exactly one explicit schema request, before the existing fixed command."""
    def __init__(self):
        self.lock = threading.Lock()
        self.issued = False
        self.witness = None

    def advance(self, messages, available):
        with self.lock:
            last_user = max((i for i, row in enumerate(messages) if row['role'] == 'user'), default=-1)
            results = [row for row in messages[last_user + 1:]
                       if row['role'] == 'tool' and row.get('tool_call_id') == LOAD_ID]
            if not self.issued:
                if results or 'command' in available or 'request_tools' not in available:
                    raise AcquisitionRefusal('acquisition-surface-invalid')
                self.issued = True
                return {'id': LOAD_ID, 'type': 'function', 'function': {
                    'name': 'request_tools', 'arguments': json.dumps({'need': 'command'})}}
            if len(results) != 1:
                raise AcquisitionRefusal('acquisition-result-missing')
            try:
                calls = [call for row in messages[last_user + 1:] if row['role'] == 'assistant'
                         for call in row.get('tool_calls', []) if isinstance(call, dict) and call.get('id') == LOAD_ID]
                if (len(calls) != 1 or calls[0].get('type') != 'function'
                        or calls[0].get('function', {}).get('name') != 'request_tools'
                        or object_json(calls[0]['function']['arguments']) != {'need': 'command'}):
                    raise ValueError('acquisition result has no exact originating call')
                witness = result_payload(results[0].get('content'))
            except (ValueError, TypeError, KeyError, AttributeError):
                raise AcquisitionRefusal('acquisition-result-invalid') from None
            if 'command' not in available:
                raise AcquisitionRefusal('acquisition-command-not-offered')
            if self.witness is not None and self.witness != witness:
                raise AcquisitionRefusal('acquisition-result-invalid')
            self.witness = witness
            return None

"""Closed fixture tool phases use the host's declared call/result grammar."""
from __future__ import annotations

import json
import re
import threading
import uuid

from ..installed_evidence import IDENTIFIER

REFUSALS = frozenset({'acquisition-surface-invalid', 'acquisition-result-missing',
                      'acquisition-result-invalid', 'acquisition-tool-not-offered'})
STAGES = frozenset({'phase', 'part-count', 'marker-lines', 'json', 'payload',
                    'feedback-envelope', 'receipt', 'call-count', 'call-shape', 'result-order'})


def fixed_call(tool):
    if tool == 'command':
        return {'name': tool, 'arguments': {'command': '/usr/bin/true'}}
    if tool == 'page_open':
        return {'name': tool, 'arguments': {'path': 'fixture.html'}}
    raise ValueError('fixture acquisition requires a declared fixed tool purpose')


class AcquisitionRefusal(ValueError):
    def __init__(self, code, stage='phase', count=None):
        if (code not in REFUSALS or stage not in STAGES
                or count is not None and (type(count) is not int or not 0 <= count <= 32769)):
            raise ValueError('acquisition refusal code is not declared')
        self.code = code
        self.detail = {'stage': stage, **({'count': count} if count is not None else {})}
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


def loaded_tool(payload, need):
    fixed_call(need)
    if (not isinstance(payload, dict)
            or set(payload) - {'need', 'loaded', 'already_loaded', 'nearest', 'note', 'discovery'}
            or payload.get('need') != need or 'loaded' not in payload):
        raise ValueError('acquisition result differs from the fixed tool request')
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
    if need not in names:
        raise ValueError('acquisition did not load the fixed tool schema')
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


def result_payload(content, tool, call_id, *, need):
    fixed_call(need)
    if tool not in {'request_tools', need}:
        raise ValueError('projected result differs from its selected fixed tool purpose')
    host_call_id(call_id)
    if not isinstance(content, str) or not 0 < len(content.encode()) <= 32768:
        raise ValueError('projected acquisition result exceeds its bound')
    # LocalInference's SystemPreamble folds later host system rows onto the
    # tool row. Only the original result/receipt prefix supplies case evidence.
    parts = content.split('\n\n', 2)
    if len(parts) < 2:
        raise AcquisitionRefusal('acquisition-result-invalid', 'part-count', min(len(parts), 32769))
    decoded = []
    for part, marker in zip(parts[:2], ('⟦D:tool⟧', '⟦D:host:tool_feedback⟧')):
        lines = part.split('\n')
        opaque = tool == need and marker == '⟦D:tool⟧'
        if (len(lines) < 2 or not opaque and len(lines) != 2 or lines[0] != marker
                or any(not line.startswith('⟦D⟧') for line in lines[1:])):
            raise AcquisitionRefusal('acquisition-result-invalid', 'marker-lines', min(len(lines), 32769))
        if opaque:
            # Evidence handles, output and compaction text are data. Only the
            # paired typed invocation and independent durable witness authorize completion.
            decoded.append(None)
            continue
        try:
            decoded.append(object_json(lines[1][len('⟦D⟧'):]))
        except ValueError:
            raise AcquisitionRefusal('acquisition-result-invalid', 'json') from None
    payload, feedback = decoded
    if tool == 'request_tools':
        try:
            loaded_tool(payload, need)
        except ValueError:
            raise AcquisitionRefusal('acquisition-result-invalid', 'payload') from None
    if set(feedback) != {'invocation'}:
        raise AcquisitionRefusal('acquisition-result-invalid', 'feedback-envelope', len(feedback))
    try:
        invocation_id = projected_receipt(feedback['invocation'], tool, call_id)
    except ValueError:
        raise AcquisitionRefusal('acquisition-result-invalid', 'receipt') from None
    return {'tool_call_id': call_id, 'invocation_id': invocation_id,
            **({'need': need, 'loaded': True} if tool == 'request_tools' else {})}


def fixed_history(messages, target):
    fixed_call(target)
    last_user = max((i for i, row in enumerate(messages) if row['role'] == 'user'), default=-1)
    history = messages[last_user + 1:]
    calls = [(ordinal, call) for ordinal, row in enumerate(history) if row['role'] == 'assistant'
             for call in row.get('tool_calls', [])]
    identifiers = []
    for _, call in calls:
        if (not isinstance(call, dict) or call.get('type') != 'function'
                or not isinstance(call.get('function'), dict)
                or call['function'].get('name') not in {'request_tools', target}):
            raise AcquisitionRefusal('acquisition-result-invalid')
        try:
            identifiers.append(host_call_id(call.get('id')))
        except ValueError:
            raise AcquisitionRefusal('acquisition-result-invalid') from None
    if len(set(identifiers)) != len(identifiers):
        raise AcquisitionRefusal('acquisition-result-invalid')
    return history, calls


def paired_result(messages, tool, arguments, target, expected=None):
    history, all_calls = fixed_history(messages, target)
    calls = [(ordinal, call) for ordinal, call in all_calls if call['function']['name'] == tool]
    if len(calls) != 1:
        raise AcquisitionRefusal('acquisition-result-missing', 'call-count', min(len(calls), 32769))
    assistant_ordinal, call = calls[0]
    try:
        identifier = host_call_id(call.get('id'))
        if (call.get('type') != 'function' or object_json(call['function']['arguments']) != arguments
                or expected is not None and identifier != expected):
            raise ValueError('fixture originating call differs from its declared phase')
        results = [(ordinal, row) for ordinal, row in enumerate(history) if row['role'] == 'tool'
                   and row.get('tool_call_id') == identifier]
        if len(results) != 1 or results[0][0] <= assistant_ordinal:
            raise AcquisitionRefusal('acquisition-result-invalid', 'result-order', min(len(results), 32769))
        return result_payload(results[0][1].get('content'), tool, identifier, need=target)
    except AcquisitionRefusal:
        raise
    except (ValueError, TypeError, KeyError, AttributeError):
        raise AcquisitionRefusal('acquisition-result-invalid', 'call-shape') from None


class FixedAcquisition:
    """Fixed emissions bind their subsequent host-issued call/result identities."""
    def __init__(self, tool):
        fixed_call(tool)
        self.tool = tool
        self.load_id = 'qualification-load-' + tool
        self.lock = threading.Lock()
        self.issued = self.tool_issued = self.completed = False
        self.witness = self.tool_witness = None

    @property
    def call(self):
        return fixed_call(self.tool)

    def advance(self, messages, available):
        with self.lock:
            if self.completed:
                raise AcquisitionRefusal('acquisition-result-invalid')
            if not self.issued:
                if (self.tool in available or 'request_tools' not in available
                        or any(row['role'] in {'assistant', 'tool'} for row in messages)):
                    raise AcquisitionRefusal('acquisition-surface-invalid')
                self.issued = True
                return {'id': self.load_id, 'type': 'function', 'function': {
                    'name': 'request_tools', 'arguments': json.dumps({'need': self.tool})}}
            history, calls = fixed_history(messages, self.tool)
            if not self.tool_issued and (any(call['function']['name'] == self.tool for _, call in calls)
                    or len([row for row in history if row['role'] == 'tool']) != 1):
                raise AcquisitionRefusal('acquisition-result-invalid')
            witness = paired_result(messages, 'request_tools', {'need': self.tool}, self.tool,
                                    self.witness['tool_call_id'] if self.witness else None)
            if self.tool not in available:
                raise AcquisitionRefusal('acquisition-tool-not-offered')
            if self.witness is not None and self.witness != witness:
                raise AcquisitionRefusal('acquisition-result-invalid')
            self.witness = witness
            if self.tool_issued:
                self.tool_witness = paired_result(messages, self.tool, self.call['arguments'], self.tool)
                load_result = next(i for i, row in enumerate(history) if row['role'] == 'tool'
                                   and row.get('tool_call_id') == witness['tool_call_id'])
                tool_call = next(i for i, call in calls if call['function']['name'] == self.tool)
                if load_result >= tool_call or len([row for row in history if row['role'] == 'tool']) != 2:
                    raise AcquisitionRefusal('acquisition-result-invalid')
                if self.tool_witness['tool_call_id'] == witness['tool_call_id']:
                    raise AcquisitionRefusal('acquisition-result-invalid')
                self.completed = True
            else:
                self.tool_issued = True
            return None

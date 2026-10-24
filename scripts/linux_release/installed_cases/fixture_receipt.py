"""Observe durable receipts for the two closed GUI fixture tool purposes."""
from __future__ import annotations

import json
import re

from .profile_observer import _database, identifier, rows
from .provider_acquisition import fixed_call, host_call_id, loaded_tool, object_json
from .fixture_content import read_object
from .browser_result import opened


PAGE_COLUMNS = {
    'messages': {'evidence_handles_json': 'TEXT'},
    'evidence_records': {**{name: 'TEXT' for name in ('project_id', 'session_id', 'handle',
        'kind', 'shape', 'fidelity', 'source_tool', 'superseded_by', 'content_blob_sha256')}, 'truncated': 'INTEGER'},
    'content_blob_objects': {'project_id': 'TEXT', 'sha256': 'TEXT', 'byte_size': 'INTEGER', 'stored_size': 'INTEGER'},
}


def page_record(connection, project_id, session_id, message_id, result):
    message = connection.execute('SELECT CASE WHEN length(CAST(tool_result_json AS BLOB))<=32768 THEN tool_result_json ELSE NULL END,'
        'length(CAST(evidence_handles_json AS BLOB)),'
        'CASE WHEN length(CAST(evidence_handles_json AS BLOB))<=1024 THEN evidence_handles_json ELSE NULL END '
        "FROM messages WHERE id=? AND session_id=? AND role='tool'", (message_id, session_id)).fetchall()
    if len(message) != 1 or object_json(message[0][0]) != result or type(message[0][1]) is not int or not 0 < message[0][1] <= 1024:
        raise ValueError('page evidence message differs from its bound tool receipt')
    handles = json.loads(message[0][2])
    if (not isinstance(handles, list) or len(handles) != 1 or not isinstance(handles[0], str)
            or re.fullmatch(r'page#[1-9][0-9]{0,9}', handles[0]) is None):
        raise ValueError('page evidence requires its single exact message handle')
    found = connection.execute('SELECT e.project_id,e.handle,e.kind,e.shape,e.fidelity,e.source_tool,'
        'e.truncated,e.superseded_by,e.content_blob_sha256,b.byte_size,b.stored_size '
        'FROM evidence_records e JOIN sessions s ON s.id=e.session_id AND s.project_id=e.project_id '
        'JOIN content_blob_objects b ON b.project_id=e.project_id AND b.sha256=e.content_blob_sha256 '
        'WHERE e.session_id=? AND e.project_id=? AND e.handle=? LIMIT 2',
        (session_id, project_id, handles[0])).fetchall()
    if (len(found) != 1 or found[0][:8] != (project_id, handles[0], 'page', 'surface_snapshot',
            'structured', 'page_open', 0, None)):
        raise ValueError('page evidence has no exact original untruncated record')
    return found[0]


def committed_fixture_tool(app, project_id, session_id, tool, call_id, *, target):
    identifier(project_id)
    identifier(session_id)
    host_call_id(call_id)
    selected = fixed_call(target)
    if tool not in {'request_tools', target}:
        raise ValueError('fixture receipt differs from its selected fixed tool purpose')
    owner = 'tool_surface' if tool == 'request_tools' else {'command': 'processes', 'page_open': 'browser'}[target]
    arguments = {'need': target} if tool == 'request_tools' else selected['arguments']
    safe = "CASE WHEN length(CAST(tool_result_json AS BLOB))<=1048576 THEN tool_result_json ELSE '{}' END"
    fields = ('tool_call_id', 'tool', 'outcome', 'invocation.id', 'invocation.tool',
              'invocation.tool_call_id', 'invocation.owner', 'invocation.status', 'tool_args', 'invocation.code')
    projection = ','.join("json_extract(" + safe + ",'$." + name + "')" for name in fields)
    with _database(app) as connection:
        found = rows(connection.execute('SELECT m.id,length(CAST(tool_result_json AS BLOB)),' + projection +
            ",json_type(" + safe + ",'$.invocation.invoked'),CASE WHEN json_extract(" + safe +
            ",'$.tool')='request_tools' THEN json_extract(" + safe + ",'$.content') ELSE NULL END "
            "FROM messages m JOIN sessions s ON s.id=m.session_id WHERE m.session_id=? AND s.project_id=? "
            "AND role='tool' AND tool_result_json IS NOT NULL AND tool_result_json!='' ORDER BY m.id LIMIT 257",
            (session_id, project_id)))
    if any(type(row[1]) is not int or row[1] > 1 << 20 for row in found):
        raise ValueError('fixed tool witness exceeds its bounded result contract')
    matching = [row for row in found if row[2] == call_id]
    if not matching:
        return None
    if len(matching) != 1:
        raise ValueError('fixed tool call has ambiguous durable receipts')
    (message_id, _, _, actual_tool, outcome, receipt_id, receipt_tool, receipt_call,
     receipt_owner, status, raw_arguments, code, invoked_type, content) = matching[0]
    if (actual_tool != tool or outcome != 'completed' or receipt_tool != tool
            or receipt_call != call_id or receipt_owner != owner or status != 'completed'
            or invoked_type != 'true' or code not in {None, ''} or object_json(raw_arguments) != arguments):
        raise ValueError('fixed tool has no exact successful invoked durable receipt')
    payload = None
    if tool == 'request_tools':
        loaded_tool(object_json(content), target)
    elif tool == 'page_open':
        with _database(app, required=PAGE_COLUMNS) as connection:
            result = connection.execute('SELECT CASE WHEN length(CAST(tool_result_json AS BLOB))<=32768 '
                'THEN tool_result_json ELSE NULL END FROM messages WHERE id=? AND session_id=?',
                (message_id, session_id)).fetchone()
            if result is None or not isinstance(result[0], str):
                raise ValueError('page evidence tool message disappeared or exceeded its bound')
            result = object_json(result[0])
            # Compare all already-authorized fields; annotated content supplies no authority.
            expected = {'tool': actual_tool, 'tool_call_id': call_id, 'outcome': outcome, 'tool_args': arguments}
            invocation = result.get('invocation')
            receipt = {'id': receipt_id, 'tool': tool, 'tool_call_id': call_id, 'owner': owner, 'status': 'completed'}
            if (any(result.get(key) != value for key, value in expected.items()) or not isinstance(invocation, dict)
                    or any(invocation.get(key) != value for key, value in receipt.items())
                    or invocation.get('invoked') is not True or invocation.get('code') not in {None, ''}):
                raise ValueError('page evidence receipt changed before original-body observation')
            record = page_record(connection, project_id, session_id, message_id, result)
        _, handle, _, _, _, _, _, _, digest, byte_size, stored_size = record
        raw = read_object(app, project_id, digest, byte_size, stored_size, maximum=64 << 10)
        body = json.loads(raw)
        if not isinstance(body, list) or len(body) != 1:
            raise ValueError('page evidence body differs from its original single result')
        document = object_json(body[0])
        payload = {key: document.get(key) for key in ('id', 'live_pages', 'max_pages')}
        with _database(app, required=PAGE_COLUMNS) as connection:
            if page_record(connection, project_id, session_id, message_id, result) != record:
                raise ValueError('page evidence metadata changed during physical observation')
    result = {'message_id': identifier(message_id), 'invocation_id': identifier(receipt_id),
            'tool_call_id': call_id, 'tool': tool, 'owner': owner,
            'status': 'completed', 'invoked': True, 'arguments': arguments,
            **({'payload': payload, 'evidence_handle': handle, 'content_blob_sha256': digest} if payload is not None else {})}
    if payload is not None:
        result['payload'] = opened({'tool_invocation': result})['page']
    return result

"""Observe durable receipts for the two closed GUI fixture tool purposes."""
from __future__ import annotations

from .profile_observer import _database, identifier, rows
from .provider_acquisition import fixed_call, host_call_id, loaded_tool, object_json


def committed_fixture_tool(app, session_id, tool, call_id, *, target):
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
        found = rows(connection.execute('SELECT id,length(CAST(tool_result_json AS BLOB)),' + projection +
            ",json_type(" + safe + ",'$.invocation.invoked'),CASE WHEN json_extract(" + safe +
            ",'$.tool') IN ('request_tools','page_open') THEN json_extract(" + safe + ",'$.content') ELSE NULL END "
            "FROM messages WHERE session_id=? AND role='tool' AND tool_result_json IS NOT NULL "
            "AND tool_result_json!='' ORDER BY id LIMIT 257", (session_id,)))
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
        payload = object_json(content)
    return {'message_id': identifier(message_id), 'invocation_id': identifier(receipt_id),
            'tool_call_id': call_id, 'tool': tool, 'owner': owner,
            'status': 'completed', 'invoked': True, 'arguments': arguments,
            **({'payload': payload} if payload is not None else {})}

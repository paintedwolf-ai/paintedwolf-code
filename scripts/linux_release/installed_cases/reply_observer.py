"""Witness only the fixed fixture reply and its committed content object."""
from __future__ import annotations

import hashlib

import os
from pathlib import Path
import re
import subprocess

from .profile_observer import _database, identifier, physical, profile, rows
from .package_payload import stable
from .provider import MODEL
from .records import CaseUnavailable
from .provider_acquisition import LOAD_ID, loaded_command, object_json

REPLY = b'{"content":"Fixture complete."}'
DIGEST = hashlib.sha256(REPLY).hexdigest()


def committed_reply(app, project_id, session_id, provider_id):
    identifier(project_id)
    identifier(session_id)
    if not isinstance(provider_id, str) or not re.fullmatch(r'[A-Za-z0-9_.-]{1,128}', provider_id):
        raise ValueError('reply witness requires its selected fixture provider')
    with _database(app) as connection:
        found = rows(connection.execute("SELECT o.id,o.message_id,o.content_blob_sha256,b.byte_size,b.stored_size "
            "FROM model_outputs o JOIN content_blob_objects b ON b.project_id=o.project_id AND b.sha256=o.content_blob_sha256 "
            "JOIN sessions s ON s.id=o.session_id AND s.project_id=o.project_id "
            "WHERE o.project_id=? AND o.session_id=? AND o.provider_id=? AND o.model=? "
            "AND o.finish_reason='stop' AND o.settled_at!='' AND o.scripted=0 AND s.status='idle' "
            "ORDER BY o.id LIMIT 257", (project_id, session_id, provider_id, MODEL)))
    if not found:
        return None
    matches = [row for row in found if row[2] == DIGEST]
    if len(matches) != 1:
        raise ValueError('settled fixture reply differs from its fixed content identity')
    output_id, message_id, digest, byte_size, stored_size = matches[0]
    identifier(output_id)
    identifier(message_id)
    if byte_size != len(REPLY) or type(stored_size) is not int or not 0 < stored_size <= 4096:
        raise ValueError('committed fixture object metadata exceeds its fixed bound')
    with profile(app) as directory:
        parents = [directory / 'projects', directory / 'projects' / project_id]
        parents.extend([parents[-1] / 'model-content', parents[-1] / 'model-content' / digest[:2]])
        for parent in parents:
            physical(parent, True)
        path = parents[-1] / digest[2:]
        before = physical(path)
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(descriptor, 'rb') as stream:
            opened = os.fstat(stream.fileno())
            if opened.st_size != stored_size:
                raise ValueError('committed fixture object size differs from metadata')
            try:
                child = subprocess.Popen(['zstd', '--decompress', '--stdout', '--quiet'], stdin=stream,
                    stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
            except FileNotFoundError:
                raise CaseUnavailable('fixed-reply-zstd-observer-unavailable') from None
            try:
                raw = child.stdout.read(len(REPLY) + 1)
                if raw != REPLY:
                    raise ValueError('committed fixture object does not contain the fixed reply')
                if child.wait(timeout=5) != 0:
                    raise ValueError('committed fixture object failed decompression')
            finally:
                if child.poll() is None:
                    child.kill()
                    child.wait()
                child.stdout.close()
            if stable(opened) != stable(os.fstat(stream.fileno())) or stable(opened) != stable(path.lstat()):
                raise ValueError('committed fixture object changed during decompression')
        if physical(path) != before:
            raise ValueError('committed fixture object changed during observation')
    return {'project_id': project_id, 'session_id': session_id, 'output_id': output_id,
            'message_id': message_id, 'content_blob_sha256': digest, 'byte_size': byte_size,
            'stored_size': stored_size, 'fixed_reply_verified': True}


def committed_fixed_tool(app, session_id, tool):
    identifier(session_id)
    declarations = {'request_tools': (LOAD_ID, 'tool_surface', {'need': 'command'}),
                    'command': ('qualification-0', 'processes', {'command': '/usr/bin/true'})}
    if not isinstance(tool, str) or tool not in declarations:
        raise ValueError('reply tool witness requires its fixed declared tool')
    call_id, owner, arguments = declarations[tool]
    safe = "CASE WHEN length(CAST(tool_result_json AS BLOB))<=1048576 THEN tool_result_json ELSE '{}' END"
    fields = ('tool_call_id', 'tool', 'outcome', 'invocation.id', 'invocation.tool',
              'invocation.tool_call_id', 'invocation.owner', 'invocation.status', 'tool_args')
    projection = ','.join("json_extract(" + safe + ",'$." + name + "')" for name in fields)
    with _database(app) as connection:
        found = rows(connection.execute('SELECT id,length(CAST(tool_result_json AS BLOB)),' + projection +
            ",json_type(" + safe + ",'$.invocation.invoked'),CASE WHEN json_extract(" + safe +
            ",'$.tool')='request_tools' THEN json_extract(" + safe + ",'$.content') ELSE NULL END "
            "FROM messages WHERE session_id=? AND role='tool' AND tool_result_json IS NOT NULL "
            "AND tool_result_json!='' ORDER BY id LIMIT 257", (session_id,)))
    if any(type(row[1]) is not int or row[1] > 1 << 20 for row in found):
        raise ValueError('fixed tool witness exceeds its bounded result contract')
    selected = [row for row in found if row[2] == call_id]
    if not selected:
        return None
    if len(selected) != 1:
        raise ValueError('fixed tool call has ambiguous durable receipts')
    (message_id, _, _, actual_tool, outcome, receipt_id, receipt_tool, receipt_call,
     receipt_owner, status, raw_arguments, invoked_type, content) = selected[0]
    if (actual_tool != tool or outcome != 'completed' or receipt_tool != tool
            or receipt_call != call_id or receipt_owner != owner or status != 'completed'
            or invoked_type != 'true' or object_json(raw_arguments) != arguments):
        raise ValueError('fixed tool has no exact successful invoked durable receipt')
    if tool == 'request_tools':
        loaded_command(object_json(content))
    return {'message_id': identifier(message_id), 'invocation_id': identifier(receipt_id),
            'tool_call_id': call_id, 'tool': tool, 'owner': owner,
            'status': 'completed', 'invoked': True, 'arguments': arguments}

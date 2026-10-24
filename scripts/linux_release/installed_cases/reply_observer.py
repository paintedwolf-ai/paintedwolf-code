"""Witness only the fixed fixture reply and its committed content object."""
from __future__ import annotations

import hashlib

import re

from .profile_observer import _database, identifier, rows
from .fixture_content import read_object
from .provider import MODEL

REPLY = b'{"content":"Fixture complete.","tool_calls_json":[],"reasoning_json":null}'
DIGEST = hashlib.sha256(REPLY).hexdigest()
TERMINAL_COLUMNS = {
    'sessions': {'parent_session_id': 'TEXT'},
    'turns': dict.fromkeys(('id', 'project_id', 'session_id', 'active_attempt_id', 'final_output_id',
                            'origin', 'status', 'completed_at'), 'TEXT'),
    'turn_attempts': dict.fromkeys(('id', 'turn_id', 'status', 'phase', 'completed_at'), 'TEXT'),
    'model_outputs': {**dict.fromkeys(('id', 'message_id', 'content_blob_sha256', 'project_id',
        'session_id', 'provider_id', 'model', 'settled_at', 'turn_attempt_id'), 'TEXT'), 'scripted': 'INTEGER'},
    'content_blob_objects': {'project_id': 'TEXT', 'sha256': 'TEXT', 'byte_size': 'INTEGER', 'stored_size': 'INTEGER'},
}


def committed_reply(app, project_id, session_id, provider_id):
    identifier(project_id)
    identifier(session_id)
    if not isinstance(provider_id, str) or not re.fullmatch(r'[A-Za-z0-9_.-]{1,128}', provider_id):
        raise ValueError('reply witness requires its selected fixture provider')
    with _database(app, required=TERMINAL_COLUMNS) as connection:
        found = rows(connection.execute("SELECT o.id,o.message_id,o.content_blob_sha256,b.byte_size,b.stored_size,t.id,a.id "
            "FROM model_outputs o JOIN content_blob_objects b ON b.project_id=o.project_id AND b.sha256=o.content_blob_sha256 "
            "JOIN sessions s ON s.id=o.session_id AND s.project_id=o.project_id "
            "JOIN turns t ON t.final_output_id=o.id AND t.project_id=o.project_id AND t.session_id=o.session_id "
            "JOIN turn_attempts a ON a.id=t.active_attempt_id AND a.turn_id=t.id AND a.id=o.turn_attempt_id "
            "WHERE o.project_id=? AND o.session_id=? AND o.provider_id=? AND o.model=? "
            "AND o.settled_at!='' AND o.scripted=0 AND s.status='idle' AND s.parent_session_id IS NULL "
            "AND ((s.provider_id IS NULL AND s.model IS NULL) OR (s.provider_id=? AND s.model=?)) "
            "AND t.origin='user' AND t.status='complete' AND t.completed_at!='' "
            "AND a.status='complete' AND a.phase='complete' AND a.completed_at!='' "
            "ORDER BY o.id LIMIT 257", (project_id, session_id, provider_id, MODEL, provider_id, MODEL)))
    if not found:
        return None
    matches = [row for row in found if row[2] == DIGEST]
    if len(matches) != 1:
        raise ValueError('settled fixture reply differs from its fixed content identity')
    output_id, message_id, digest, byte_size, stored_size, turn_id, attempt_id = matches[0]
    identifier(turn_id)
    identifier(attempt_id)
    identifier(output_id)
    identifier(message_id)
    if byte_size != len(REPLY) or type(stored_size) is not int or not 0 < stored_size <= 4096:
        raise ValueError('committed fixture object metadata exceeds its fixed bound')
    raw = read_object(app, project_id, digest, byte_size, stored_size, maximum=len(REPLY))
    if raw != REPLY:
        raise ValueError('committed fixture object does not contain the fixed reply')
    return {'project_id': project_id, 'session_id': session_id, 'output_id': output_id,
            'message_id': message_id, 'turn_id': turn_id, 'attempt_id': attempt_id, 'content_blob_sha256': digest, 'byte_size': byte_size,
            'stored_size': stored_size, 'fixed_reply_verified': True}

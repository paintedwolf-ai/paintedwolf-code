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

"""Closed app-password fixture metadata; secret frames never use evidence encoding."""
from __future__ import annotations

from . import lineage, protocol

STAGES = ('new-app-password', 'confirm-app-password', 'native-presence-password')
SECRET_BYTES = 40
MAX_SECONDS = 20


def actor(value):
    if (not isinstance(value, dict) or set(value) != {'process', 'uid', 'session_nonce'}
            or type(value['uid']) is not int or value['uid'] <= 0):
        raise ValueError('password fixture requires its independently selected SSH actor')
    lineage.identity(value['process'])
    protocol.nonce(value['session_nonce'])
    return value


def private_secret(value):
    # This validates a private frame only. Never include the rejected value in diagnostics.
    if (not isinstance(value, str) or len(value) != SECRET_BYTES
            or not value.isascii() or not value.isalnum()):
        raise ValueError('password fixture private frame has an invalid fixed shape')
    return value

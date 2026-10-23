"""Closed app-password fixture metadata; secret frames never use evidence encoding."""
from __future__ import annotations

import struct

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


LABELS = {'new-app-password': 'New app password', 'confirm-app-password': 'Confirm app password'}
OPERATION = 'app-password-fixture-v1'


def target(stage, value):
    from . import surface_wire
    if stage not in STAGES or not isinstance(value, dict):
        raise ValueError('password fixture requires a closed stage and observed target')
    if stage in LABELS:
        if set(value) != {'kind', 'label', 'focus'} or value['kind'] != 'entry' or value['label'] != LABELS[stage]:
            raise ValueError('password setup requires its exact named password entry')
        protocol.focus(value['focus'])
        return value
    if set(value) != {'kind', 'surface', 'password'} or value['kind'] != 'native-presence':
        raise ValueError('password confirmation requires its actual owned native modal')
    proof = surface_wire.before('presence-cancel-v1', value['surface'])
    control = value['password']
    if (not isinstance(control, dict) or set(control) != {'identity', 'parent', 'focused', 'visible'}
            or control['focused'] is not True or control['visible'] is not True):
        raise ValueError('native password input is not the observed focused enabled control')
    identity = surface_wire.object_identity(control['identity'], proof['launch'])
    dialog = next(row for row in proof['surfaces'] if surface_wire.active(row))
    if (control['parent'] != dialog['identity'] or identity['process'] != proof['main']['process']
            or identity['bus_name'] != proof['main']['bus_name']
            or identity in [row['identity'] for row in proof['surfaces']]):
        raise ValueError('native password control escaped its captured modal parent')
    return value


def request(value, execution):
    fields = {'format_version', 'binding', 'operation', 'stage', 'transaction_id', 'actor',
              'case', 'profile', 'session', 'target', 'expires_ns'}
    if (not isinstance(value, dict) or set(value) != fields or value['format_version'] != 1
            or type(value['format_version']) is not int or value['binding'] != protocol.binding(execution)
            or value['operation'] != OPERATION or value['stage'] not in STAGES):
        raise ValueError('private fixture request differs from its fixed execution and stage')
    protocol.nonce(value['transaction_id'])
    selected = actor(value['actor'])
    lineage.identity(value['case'])
    profile = value['profile']
    from pathlib import Path
    if (not isinstance(profile, dict) or set(profile) != {'path', 'device', 'inode', 'uid'}
            or not isinstance(profile['path'], str) or len(profile['path']) > 512
            or not Path(profile['path']).is_absolute() or '\0' in profile['path']
            or any(type(profile[key]) is not int or profile[key] < 0 for key in ('device', 'inode', 'uid'))
            or profile['inode'] <= 0 or profile['uid'] != selected['uid']):
        raise ValueError('private fixture request has no fixed owned profile identity')
    session = value['session']
    required = {'id', 'User', 'Type', 'Active', 'Remote'}
    allowed = required | {'Desktop', 'Class', 'Display', 'Seat', 'DesktopOwner'}
    if (not isinstance(session, dict) or not required <= set(session) or set(session) - allowed
            or any(not isinstance(item, str) or len(item) > 256
                   for key, item in session.items() if key != 'DesktopOwner')
            or 'DesktopOwner' in session and not isinstance(session['DesktopOwner'], dict)
            or not session['id'].isalnum() or session['User'] != str(selected['uid'])
            or session['Type'] not in {'x11', 'wayland'} or session['Active'] != 'yes' or session['Remote'] != 'no'):
        raise ValueError('private fixture request has no selected active owned graphical session')
    # Runtime admission compares the complete value with graphical_session();
    # desktop-owner acquisition and its native identity policy remain there.
    if type(value['expires_ns']) is not int or value['expires_ns'] <= 0:
        raise ValueError('private fixture request has no bounded one-use deadline')
    target(value['stage'], value['target'])
    protocol.canonical(value)
    return value


def frame(raw, maximum):
    if not isinstance(raw, bytes) or not 0 < len(raw) <= maximum:
        raise ValueError('private fixture frame exceeded its fixed bound')
    return struct.pack('!I', len(raw)) + raw


def frame_size(header, maximum):
    if not isinstance(header, bytes) or len(header) != 4:
        raise ValueError('private fixture frame has no complete bounded header')
    size, = struct.unpack('!I', header)
    if not 0 < size <= maximum:
        raise ValueError('private fixture frame exceeded its fixed bound')
    return size


def completed(value, pending, vm_id):
    """Correlate aggregate delivery; host lease verification remains on the host."""
    fields = {'format_version', 'execution_nonce', 'transaction_id', 'stage', 'actor', 'case',
              'profile', 'session', 'target', 'acknowledged_count', 'completed', 'delivery_uncertain', 'lease'}
    expected = {'format_version': 1, 'execution_nonce': pending['binding']['execution_nonce'],
                'transaction_id': pending['transaction_id'], 'stage': pending['stage'],
                **{key: pending[key] for key in ('actor', 'case', 'profile', 'session', 'target')},
                'acknowledged_count': SECRET_BYTES + 1, 'completed': True, 'delivery_uncertain': False}
    if (not isinstance(value, dict) or set(value) != fields
            or protocol.canonical({key: value[key] for key in expected}) != protocol.canonical(expected)
            or not isinstance(value['lease'], dict) or value['lease'].get('vm_id') != vm_id):
        raise ValueError('private fixture completion differs from its captured physical transaction')
    protocol.canonical(value)
    return value

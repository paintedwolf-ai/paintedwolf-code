"""Closed native GNOME prompt proof; application frames cannot authorize OS input."""
from __future__ import annotations

import hashlib
import re

from . import accessibility, session_wire

PURPOSE = 'native-gnome-qa-unlock-v1'
REVEAL = 'native-gnome-qa-reveal-v1'


def target(value):
    if not isinstance(value, dict) or set(value) != {'session', 'window', 'password', 'ancestors', 'window_state', 'password_state'}:
        raise ValueError('native unlock target has an unknown shape')
    session_wire.proof(value['session'])
    shell = value['session']['native_session']['authorities']['shell']
    accessibility.validate_identity(value['window'])
    if value['window']['pid'] != shell['pid']:
        raise ValueError('native unlock window is not the captured GNOME shell')
    state = value['window_state']
    if (not isinstance(state, dict) or set(state) != {'active', 'focused', 'visible', 'enabled', 'sensitive', 'defunct'}
            or any(type(item) is not bool for item in state.values()) or not state['active']
            or not all(state[key] for key in ('visible', 'enabled', 'sensitive')) or state['defunct']):
        raise ValueError('native unlock window has no positive live active surface proof')
    password, chain = value['password'], value['ancestors']
    if password is None:
        if chain != [] or value['password_state'] is not None or not state['focused']:
            raise ValueError('native clock proof cannot claim a password ancestor chain')
    else:
        password_state = value['password_state']
        if (not isinstance(password_state, dict) or set(password_state) != {'focused', 'visible', 'enabled', 'sensitive', 'defunct'}
                or any(type(item) is not bool for item in password_state.values()) or password_state['defunct']
                or not all(password_state[key] for key in ('focused', 'visible', 'enabled', 'sensitive'))):
            raise ValueError('native password has no positive live focus proof')
        accessibility.validate_identity(password)
        accessibility.validate(chain, shell['pid'], shell['pid'])
        if (chain[0] != password or chain[-1] != value['window']
                or any(row['pid'] != shell['pid'] or row['bus_name'] != value['window']['bus_name'] for row in chain)):
            raise ValueError('native password does not belong to the captured shell window')
    return value


def locked(value, *, password):
    target(value)
    if (not all(value['session']['native_session'][key] for key in ('locked_hint', 'locker_active'))
            or (value['password'] is not None) is not password):
        raise ValueError('native unlock input requires its exact locked target phase')
    return value


def same_runtime(before, after):
    target(before)
    target(after)
    session_wire.same_runtime(before['session'], after['session'])
    if before['window'] != after['window']:
        raise ValueError('native unlock replaced its captured shell window')


def secret(raw):
    if (not isinstance(raw, (bytes, bytearray)) or len(raw) != 40
            or any(not (48 <= byte <= 57 or 65 <= byte <= 90 or 97 <= byte <= 122) for byte in raw)):
        raise ValueError('OS fixture credential has an invalid closed shape')
    return raw


def secret_frame(raw):
    import struct
    secret(raw)
    return struct.pack('!I', 40) + bytes(raw)


def read_secret_frame(descriptor=0, *, timeout=5):
    import os
    import selectors
    import struct
    import time
    if type(timeout) not in {int, float} or not 0 < timeout <= 30:
        raise ValueError('OS fixture credential requires its bounded frame deadline')
    buffer = bytearray()
    try:
        if hasattr(descriptor, 'read') and not isinstance(descriptor, int):
            chunk = descriptor.read(45)
            if isinstance(chunk, str):
                raise ValueError('OS fixture credential frame must be binary')
            buffer.extend(chunk)
        else:
            fd = descriptor if isinstance(descriptor, int) else descriptor.fileno()
            deadline = time.monotonic() + timeout
            blocking = os.get_blocking(fd)
            try:
                os.set_blocking(fd, False)
                with selectors.DefaultSelector() as selector:
                    selector.register(fd, selectors.EVENT_READ)
                    while len(buffer) < 45:
                        remaining = deadline - time.monotonic()
                        if remaining <= 0:
                            raise ValueError('OS fixture credential frame timed out')
                        if not selector.select(timeout=min(0.25, remaining)):
                            continue
                        try:
                            chunk = os.read(fd, 45 - len(buffer))
                        except (BlockingIOError, InterruptedError):
                            continue
                        if not chunk:
                            break
                        buffer.extend(chunk)
            finally:
                os.set_blocking(fd, blocking)
        if len(buffer) == 44 and buffer[:4] == struct.pack('!I', 40):
            payload = bytearray(buffer[4:])
        elif len(buffer) == 40:
            payload = bytearray(buffer)
        else:
            raise ValueError('OS fixture credential frame has an invalid length')
        secret(payload)
        return payload
    finally:
        buffer[:] = b'\0' * len(buffer)


PREPARE = 'native-gnome-qa-prepare-v1'
OPERATIONS = {PREPARE, PURPOSE}


def request(value, execution):
    from . import protocol
    if (not isinstance(value, dict) or set(value) != {'format_version', 'binding', 'transaction_id',
            'operation', 'native_session', 'account', 'case'} or type(value['format_version']) is not int
            or value['format_version'] != 1 or value['binding'] != protocol.binding(execution)
            or value['operation'] not in OPERATIONS):
        raise ValueError('OS input requires its closed current execution request')
    from ..installed_cases.shipping_lifecycle import KEEP_AWAKE_CASES, PUBLIC_CASE_MODES
    case = value['case']
    if not isinstance(case, str) or case not in KEEP_AWAKE_CASES:
        raise ValueError('OS input requires its exact declared unlock case')
    selection = value['binding']['case_selection']
    selected = [] if selection is None else [item['case'] for item in selection['cases']
               if item['capability'] == 'session' and item['case'] in KEEP_AWAKE_CASES]
    if len(selected) > 1 or selected and selected != [case] or case in PUBLIC_CASE_MODES and selected != [case]:
        raise ValueError('OS input case differs from its one-use controller selection')
    protocol.nonce(value['transaction_id'])
    native = session_wire.proof(value['native_session'])['native_session']
    account = value['account']
    if (not isinstance(account, dict) or set(account) != {'name', 'uid', 'gid', 'home'}
            or not isinstance(account['name'], str) or not re.fullmatch(r'pwqual058[a-z0-9]{0,12}', account['name'])
            or type(account['uid']) is not int or not 30000 <= account['uid'] < 1 << 31
            or type(account['gid']) is not int or account['gid'] != account['uid']
            or str(account['uid']) != native['session']['User']
            or account['home'] != '/home/' + account['name']):
        raise ValueError('OS input has no exact dedicated QA account identity')
    expected = value['operation'] == PURPOSE
    if any(native[key] is not expected for key in ('locked_hint', 'locker_active')):
        raise ValueError('OS input request differs from its required native lock phase')
    return value


def digest(value):
    from .protocol import canonical
    return hashlib.sha256(canonical(value)).hexdigest()


def acknowledgment(value, pending, vm_id):
    from . import protocol
    request(pending, pending['binding'])
    if (not isinstance(value, dict) or set(value) != {'format_version', 'request_sha256', 'vm_id', 'result'}
            or type(value['format_version']) is not int or value['format_version'] != 1
            or value['request_sha256'] != digest(pending) or value['vm_id'] != vm_id
            or not isinstance(vm_id, str) or not protocol.IDENTIFIER.fullmatch(vm_id)):
        raise ValueError('OS input acknowledgment differs from its exact request and VM')
    result = value['result']
    if pending['operation'] == PREPARE:
        if (not isinstance(result, dict) or set(result) != {'prepared', 'account'}
                or result['prepared'] is not True or result['account'] != pending['account']):
            raise ValueError('OS input has no exact prepared credential custody')
    else:
        if (not isinstance(result, dict) or set(result) != {'purpose', 'transaction_id', 'delivered',
                'text_readback', 'session_after'} or result['purpose'] != PURPOSE
                or result['transaction_id'] != pending['transaction_id']
                or result['delivered'] is not True or result['text_readback'] is not False):
            raise ValueError('OS input has no exact private transaction completion')
        session_wire.same_runtime(pending['native_session'], result['session_after'])
        if any(result['session_after']['native_session'][key] for key in ('locked_hint', 'locker_active')):
            raise ValueError('OS input delivery did not independently unlock both native flags')
    return value

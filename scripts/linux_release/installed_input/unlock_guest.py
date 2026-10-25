"""Native-target half of the one-use private OS-unlock exchange; no secret readback."""
from __future__ import annotations

import time

from . import protocol, session_wire, unlock_wire
from ..installed_cases.records import CaseUnavailable


def exchange(pipe, observer, execution, pending, *, timeout=15):
    execution = protocol.binding(execution)
    unlock_wire.request(pending, execution)
    if pending['operation'] not in {unlock_wire.PURPOSE, unlock_wire.STALE_UNLOCK}:
        raise ValueError('native unlock endpoint has a foreign phase')
    if type(timeout) not in {int, float} or not 0 < timeout <= 30:
        raise ValueError('native unlock requires its bounded target deadline')
    deadline = time.monotonic() + timeout
    request = pipe.receive_metadata()
    if (not isinstance(request, dict) or set(request) != {'purpose', 'binding', 'transaction_id', 'phase'}
            or request['purpose'] != pending['operation'] or request['binding'] != execution
            or request['phase'] != 'prompt' or request['transaction_id'] != pending['transaction_id']):
        raise ValueError('native unlock endpoint requires its closed current execution')
    protocol.nonce(request['transaction_id'])
    envelope = {key: request[key] for key in ('purpose', 'binding', 'transaction_id')}
    while True:
        try:
            initial = observer.observe()
            break
        except CaseUnavailable as error:
            if (time.monotonic() >= deadline
                    or str(error) not in {
                        'native-clock-window-has-no-positive-focus-proof',
                        'native-locked-shell-window-missing-or-ambiguous'}):
                raise
            time.sleep(0.05)
    session_wire.same_runtime(pending['native_session'], initial['session'])
    unlock_wire.locked(initial, password=initial['password'] is not None)
    pipe.send_metadata({**envelope, 'target': initial})
    if pipe.receive_metadata() != {**envelope, 'phase': 'prompt-ready'}:
        raise ValueError('native unlock endpoint has no exact prompt readiness acknowledgment')
    for ordinal in range(41):
        if pipe.receive_metadata() != {**envelope, 'ordinal': ordinal, 'phase': 'observe'}:
            raise ValueError('native unlock target observation changed its fixed ordinal')
        while True:
            try:
                current = observer.observe()
            except CaseUnavailable as error:
                if (ordinal != 0 or time.monotonic() >= deadline
                        or str(error) not in {
                            'native-clock-window-has-no-positive-focus-proof',
                            'native-locked-shell-window-missing-or-ambiguous'}):
                    raise
                time.sleep(0.05)
                continue
            unlock_wire.same_runtime(initial, current)
            if current['password'] is not None:
                break
            if ordinal != 0 or time.monotonic() >= deadline:
                raise CaseUnavailable('native-password-prompt-was-not-positively-observed')
            time.sleep(0.05)
        unlock_wire.locked(current, password=True)
        pipe.send_metadata({**envelope, 'ordinal': ordinal, 'target': current})
    if pipe.receive_metadata() != {**envelope, 'phase': 'complete'}:
        raise ValueError('native unlock endpoint has no exact final delivery acknowledgment')
    pipe.send_metadata({**envelope, 'phase': 'delivered'})
    while True:
        final = observer.session_proof()
        session_wire.same_runtime(initial['session'], final)
        if not any(final['native_session'][key] for key in ('locked_hint', 'locker_active')):
            pipe.send_metadata({**envelope, 'phase': 'unlocked', 'session_after': final})
            return {'purpose': pending['operation'], 'transaction_id': envelope['transaction_id'],
                    'before': initial, 'session_after': final, 'unlocked': True, 'text_readback': False}
        if time.monotonic() >= deadline:
            raise CaseUnavailable('native-unlock-did-not-clear-both-independent-lock-flags')
        time.sleep(0.05)


def prepare_exchange(pipe, observer, execution, pending, *, vm_id=None):
    from . import unlock_account
    execution = protocol.binding(execution)
    unlock_wire.request(pending, execution)
    if pending['operation'] != unlock_wire.PREPARE:
        raise ValueError('native credential preparation endpoint has a foreign phase')
    request = pipe.receive_metadata()
    envelope = {'purpose': unlock_wire.PREPARE, 'binding': execution,
                'transaction_id': pending['transaction_id']}
    if request != {**envelope, 'phase': 'prepare'}:
        raise ValueError('native credential preparation endpoint requires its closed current execution')
    current = observer.observe()
    session_wire.same_runtime(pending['native_session'], current)
    if any(current['native_session'][key] for key in ('locked_hint', 'locker_active')):
        raise ValueError('native credential preparation requires an unlocked session')
    secret_raw = bytearray(pipe.receive_frame(40))
    try:
        unlock_wire.secret(secret_raw)
        unlock_account.run_privileged_prepare(pending['account'], secret_raw, vm_id=vm_id)
    finally:
        secret_raw[:] = b'\0' * len(secret_raw)
    pipe.send_metadata({**envelope, 'phase': 'prepared', 'account': pending['account'],
                        'credential_state': 'active'})
    return {'prepared': True, 'account': dict(pending['account'])}


def serve_prepare(pending, execution, vm_id, mailbox):
    """Switch only the already attached SSH stream into the closed credential preparation."""
    import os
    import selectors
    import sys
    from types import SimpleNamespace
    from .private_pipe import Pipe
    from .session_lock import NativeLockObserver
    from ..installed_environment import graphical_session
    unlock_wire.request(pending, execution)
    if pending['operation'] != unlock_wire.PREPARE or mailbox.read('request.json') != pending:
        raise ValueError('native credential preparation lost its exact published request')
    mailbox.attach()
    session = graphical_session()
    if session['User'] != str(pending['account']['uid']):
        raise ValueError('native credential preparation endpoint has a foreign current account')
    with NativeLockObserver(session) as lock:
        session_wire.same_runtime(pending['native_session'], lock.observe())
        sys.stdout.buffer.write(protocol.canonical({'state': 'native-prepare-ready',
            'request_sha256': unlock_wire.digest(pending), 'vm_id': vm_id}))
        sys.stdout.buffer.flush()
        streams = SimpleNamespace(stdin=sys.stdout.buffer, stdout=sys.stdin.buffer)
        previous = [os.get_blocking(stream.fileno()) for stream in (streams.stdin, streams.stdout)]
        try:
            for stream in (streams.stdin, streams.stdout):
                os.set_blocking(stream.fileno(), False)
            with selectors.DefaultSelector() as selector:
                selector.register(streams.stdout, selectors.EVENT_READ)
                result = prepare_exchange(Pipe(streams, selector, time.monotonic() + 20),
                                          lock, execution, pending, vm_id=vm_id)
            if mailbox.read('request.json') != pending:
                raise ValueError('native credential preparation publication changed during delivery')
            mailbox.attach()
            return result
        finally:
            for stream, blocking in zip((streams.stdin, streams.stdout), previous):
                os.set_blocking(stream.fileno(), blocking)


def serve_mailbox(pending, execution, vm_id, mailbox):
    """Switch only the already attached SSH stream into the closed native exchange."""
    import os
    import selectors
    import sys
    from types import SimpleNamespace
    from . import unlock_account
    from .private_pipe import Pipe
    from .native_unlock import NativeUnlockObserver
    from .session_lock import NativeLockObserver
    from ..installed_environment import graphical_session
    unlock_wire.request(pending, execution)
    if pending['operation'] not in {unlock_wire.PURPOSE, unlock_wire.STALE_UNLOCK} or mailbox.read('request.json') != pending:
        raise ValueError('native unlock lost its exact published request')
    mailbox.attach()
    session = graphical_session()
    if session['User'] != str(pending['account']['uid']):
        raise ValueError('native unlock endpoint has a foreign current account')
    with NativeLockObserver(session) as lock, NativeUnlockObserver(lock) as observer:
        session_wire.same_runtime(pending['native_session'], observer.session_proof())
        sys.stdout.buffer.write(protocol.canonical({'state': 'native-unlock-ready',
            'request_sha256': unlock_wire.digest(pending), 'vm_id': vm_id}))
        sys.stdout.buffer.flush()
        streams = SimpleNamespace(stdin=sys.stdout.buffer, stdout=sys.stdin.buffer)
        previous = [os.get_blocking(stream.fileno()) for stream in (streams.stdin, streams.stdout)]
        try:
            for stream in (streams.stdin, streams.stdout):
                os.set_blocking(stream.fileno(), False)
            with selectors.DefaultSelector() as selector:
                selector.register(streams.stdout, selectors.EVENT_READ)
                result = exchange(Pipe(streams, selector, time.monotonic() + 30), observer,
                                  execution, pending, timeout=25)
            if mailbox.read('request.json') != pending:
                raise ValueError('native unlock publication changed during private delivery')
            mailbox.attach()
            return result
        finally:
            for stream, blocking in zip((streams.stdin, streams.stdout), previous):
                os.set_blocking(stream.fileno(), blocking)
            if pending['operation'] == unlock_wire.PURPOSE:
                unlock_account.run_privileged_disable(pending['account'], vm_id=vm_id)

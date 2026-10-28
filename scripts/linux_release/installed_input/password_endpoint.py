"""Authenticated SSH endpoint relays private bytes without disk or per-key evidence."""
from __future__ import annotations

import os
from pathlib import Path
import time

from ..installed_cases.app import process_identity
from ..installed_cases.profile_observer import physical
from ..campaign.environment import graphical_session
from . import password_wire
from .password_exchange import observed
from .password_socket import connect


def relay(mailbox, pending, selected_actor, pipe):
    password_wire.request(pending, mailbox.execution)
    if (pending['actor'] != selected_actor or selected_actor['uid'] != os.getuid()
            or process_identity(os.getpid()) != selected_actor['process']
            or process_identity(pending['case']['pid']) != pending['case']):
        raise ValueError('private fixture differs from its independently captured peers')
    profile = pending['profile']
    info = physical(Path(profile['path']), True)
    if info[:3] != (profile['device'], profile['inode'], profile['uid']) or graphical_session() != pending['session']:
        raise ValueError('private fixture differs from its captured profile or graphical session')
    # expires_ns was created by the case on this same guest monotonic clock.
    deadline = pending['expires_ns'] / 1_000_000_000
    if not 0 < deadline - time.monotonic() <= password_wire.MAX_SECONDS:
        raise ValueError('private fixture guest lifetime expired or exceeded its fixed bound')
    pipe.deadline = min(pipe.deadline, deadline)
    expected = {'process': pending['case'], 'uid': selected_actor['uid'],
                'session_nonce': selected_actor['session_nonce']}
    value = None
    try:
        with connect(mailbox, expected, deadline) as channel:
            if channel.receive_metadata() != {'request': pending}:
                raise ValueError('private fixture case stream differs from its published metadata')
            value = channel.receive_fixture_value()
            pipe.send_frame(password_wire.private_secret(value).encode('ascii'), 128)
            value = None
            for ordinal in range(password_wire.SECRET_BYTES + 1):
                for operation in ('observe-before', 'observe-after'):
                    command = {'operation': operation, 'ordinal': ordinal}
                    if pipe.receive_metadata() != command:
                        raise ValueError('private fixture host sequence changed or replayed')
                    channel.send_metadata(command)
                    fact = channel.receive_metadata()
                    if fact != observed(ordinal, pending):
                        raise ValueError('private fixture case lost its selected target')
                    pipe.send_metadata(fact)
            finished = pipe.receive_metadata()
            if not isinstance(finished, dict) or set(finished) != {'operation', 'delivery'} or finished['operation'] != 'finished':
                raise ValueError('private fixture host omitted its bounded completion')
            password_wire.completed(finished['delivery'], pending, mailbox.vm_id)
            channel.send_metadata(finished)
            accepted = channel.receive_metadata()
            if accepted != {'operation': 'accepted', 'transaction_id': pending['transaction_id']}:
                raise ValueError('private fixture case did not accept its physical completion')
            pipe.send_metadata(accepted)
    finally:
        value = None


def serve(mailbox, pending, selected_actor):
    """Only this authenticated SSH process owns these standard pipe descriptors."""
    import selectors
    import sys
    from types import SimpleNamespace
    from .private_pipe import Pipe
    from . import protocol
    streams, selector, succeeded = [], None, False
    try:
        process = SimpleNamespace(stdin=sys.stdout.buffer, stdout=sys.stdin.buffer)
        for stream in (process.stdin, process.stdout):
            streams.append((stream.fileno(), os.get_blocking(stream.fileno())))
            os.set_blocking(stream.fileno(), False)
        selector = selectors.DefaultSelector()
        selector.register(process.stdout, selectors.EVENT_READ)
        pipe = Pipe(process, selector, time.monotonic() + password_wire.MAX_SECONDS)
        pipe.write(protocol.canonical({'state': 'private-fixture-open'}))
        relay(mailbox, pending, selected_actor, pipe)
        succeeded = True
    except BaseException:
        # No exception arguments or traceback frames enter the SSH error log.
        succeeded = False
    finally:
        if selector is not None:
            selector.close()
        for descriptor, blocking in streams:
            try:
                os.set_blocking(descriptor, blocking)
            except OSError:
                succeeded = False
    return succeeded

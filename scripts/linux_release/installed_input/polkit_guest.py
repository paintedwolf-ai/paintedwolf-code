"""Read only the actual guest agent target; credentials stay with the physical actor."""
from __future__ import annotations

import os
import selectors
import sys
import time
from types import SimpleNamespace

from . import polkit_control, protocol
from .polkit_stage import CANCEL, PURPOSE
from .polkit_target import Target
from .private_pipe import Pipe
from ..campaign.environment import graphical_session


def exchange(pipe, target, pending):
    envelope = {'purpose': pending['operation'], 'binding': pending['binding'],
                'transaction_id': pending['transaction_id']}
    for ordinal in range(41 if pending['operation'] == PURPOSE else 1):
        row = pipe.receive_metadata()
        if (not isinstance(row, dict) or set(row) != {*envelope, 'ordinal', 'phase', 'pending'}
                or any(row[key] != value for key, value in envelope.items())
                or type(row['ordinal']) is not int or row['ordinal'] != ordinal or row['phase'] != 'observe'):
            raise ValueError('Polkit target exchange replaced its closed physical observation')
        pipe.send_metadata({**envelope, 'ordinal': ordinal, 'target': target.observe(row['pending'])})
    if pipe.receive_metadata() != {**envelope, 'phase': 'complete'}:
        raise ValueError('Polkit target exchange has no final physical delivery acknowledgment')
    pipe.send_metadata({**envelope, 'phase': 'delivered'})


def serve(mailbox, pending, vm_id):
    polkit_control.request(pending, mailbox.execution)
    if pending['operation'] not in {PURPOSE, CANCEL} or mailbox.read('request.json') != pending:
        raise ValueError('Polkit input endpoint has no exact current mailbox publication')
    mailbox.attach()
    session = graphical_session()
    if session['User'] != str(pending['account']['uid']):
        raise ValueError('Polkit target endpoint has a foreign actual graphical account')
    with Target(session) as target:
        sys.stdout.buffer.write(protocol.canonical({'state': 'native-polkit-ready',
            'request_sha256': protocol.request_sha256(pending), 'vm_id': vm_id}))
        sys.stdout.buffer.flush()
        streams = SimpleNamespace(stdin=sys.stdout.buffer, stdout=sys.stdin.buffer)
        previous = [os.get_blocking(stream.fileno()) for stream in (streams.stdin, streams.stdout)]
        try:
            for stream in (streams.stdin, streams.stdout):
                os.set_blocking(stream.fileno(), False)
            with selectors.DefaultSelector() as selector:
                selector.register(streams.stdout, selectors.EVENT_READ)
                exchange(Pipe(streams, selector, time.monotonic() + 30), target, pending)
            mailbox.attach()
            if mailbox.read('request.json') != pending:
                raise ValueError('Polkit target publication changed during private delivery')
        finally:
            for stream, blocking in zip((streams.stdin, streams.stdout), previous):
                os.set_blocking(stream.fileno(), blocking)

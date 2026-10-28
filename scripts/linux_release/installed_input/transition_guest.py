"""Deliver closed native transitions with their independent observation authorities."""
from __future__ import annotations

import secrets
import time

from . import protocol, session_wire, surface_wire
from .mailbox import Mailbox
from ..installed_cases.records import CaseUnavailable


def perform_transition(execution, vm_id, observer, operation, mailbox=None):
    if operation in surface_wire.OPERATIONS:
        wire, proof_key, after_key = surface_wire, 'surface', 'surface_after'
    elif operation in session_wire.OPERATIONS:
        wire, proof_key, after_key = session_wire, 'native_session', 'native_session_after'
    else:
        raise ValueError('native transition input requires a closed declared operation')
    sequence = protocol.steps(operation)
    mailbox = mailbox or Mailbox(execution, vm_id)
    mailbox.attach()
    transaction, events = secrets.token_hex(16), []
    initial = observer.prepare(operation)
    try:
        for ordinal, _ in enumerate(sequence):
            before = observer.prepare(operation)
            wire.same_runtime(initial, before)
            pending = protocol.request({'format_version': 1, 'binding': execution,
                'transaction_id': transaction, 'operation': operation,
                'ordinal': ordinal, proof_key: before}, execution)
            mailbox.write('request.json', pending)
            deadline = time.monotonic() + 10
            while True:
                acknowledgment = mailbox.read('ack.json')
                if acknowledgment is not None:
                    delivery = protocol.acknowledgment(acknowledgment, pending, vm_id)
                    break
                observer.during(operation, initial, deadline)
                if time.monotonic() >= deadline:
                    raise CaseUnavailable('fixed-VM-transition-input-acknowledgment-unavailable')
                time.sleep(0.05)
            try:
                after = observer.settle(operation, initial) if ordinal == len(sequence) - 1 else observer.observe()
            except BaseException as error:
                events.append({'request': pending, 'delivery': delivery,
                               'surface_after': getattr(error, 'observations', {}).get('surface_after')})
                raise
            if ordinal == len(sequence) - 1:
                if not wire.finished(operation, initial, after):
                    raise ValueError('physical input did not establish its declared native transition')
            else:
                wire.same_runtime(initial, after)
            events.append({'request': pending, 'delivery': delivery, after_key: after})
            mailbox.clear_completed(pending)
    except BaseException as error:
        error.observations = {**getattr(error, 'observations', {}), 'native_input': {
            'format_version': 1, 'vm_id': vm_id, 'transaction_id': transaction, 'operation': operation,
            'completed': False, 'events': events}}
        raise
    return {'format_version': 1, 'vm_id': vm_id, 'transaction_id': transaction, 'operation': operation,
            'completed': True, 'events': events, 'text_readback': False}

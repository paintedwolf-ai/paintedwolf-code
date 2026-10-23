"""Deliver closed frame actions with transition-aware observation, never editor focus relaxation."""
from __future__ import annotations

import secrets
import time

from . import protocol, surface_wire
from .mailbox import Mailbox
from ..installed_cases.records import CaseUnavailable


def perform_transition(execution, vm_id, observer, operation, mailbox=None):
    if operation not in surface_wire.OPERATIONS:
        raise ValueError('frame input requires a closed surface operation')
    sequence = protocol.steps(operation)
    mailbox = mailbox or Mailbox(execution, vm_id)
    mailbox.attach()
    transaction, events = secrets.token_hex(16), []
    initial = observer.prepare(operation)
    try:
        for ordinal, _ in enumerate(sequence):
            before = observer.prepare(operation)
            surface_wire.same_runtime(initial, before)
            pending = protocol.request({'format_version': 1, 'binding': execution,
                'transaction_id': transaction, 'operation': operation,
                'ordinal': ordinal, 'surface': before}, execution)
            mailbox.write('request.json', pending)
            deadline = time.monotonic() + 10
            while True:
                acknowledgment = mailbox.read('ack.json')
                if acknowledgment is not None:
                    delivery = protocol.acknowledgment(acknowledgment, pending, vm_id)
                    break
                observer.during(initial)
                if time.monotonic() >= deadline:
                    raise CaseUnavailable('fixed-VM-surface-input-acknowledgment-unavailable')
                time.sleep(0.05)
            after = observer.settle(operation, initial) if ordinal == len(sequence) - 1 else observer.observe()
            surface_wire.same_runtime(initial, after)
            events.append({'request': pending, 'delivery': delivery, 'surface_after': after})
            mailbox.clear_completed(pending)
    except BaseException as error:
        error.observations = {**getattr(error, 'observations', {}), 'native_input': {
            'format_version': 1, 'vm_id': vm_id, 'transaction_id': transaction, 'operation': operation,
            'completed': False, 'events': events}}
        raise
    return {'format_version': 1, 'vm_id': vm_id, 'transaction_id': transaction, 'operation': operation,
            'completed': True, 'events': events, 'text_readback': False}

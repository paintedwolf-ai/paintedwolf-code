"""Request sealed physical input while continuously observing the owned UI focus."""
from __future__ import annotations

import secrets
import math
import time

from . import protocol
from .mailbox import Mailbox
from ..installed_cases.records import CaseUnavailable


def perform(execution: dict, vm_id: str, actor, mailbox=None, *, operation='editor-replace-save-v1', deadline=None) -> dict:
    if deadline is not None and (type(deadline) not in (int, float) or not math.isfinite(deadline)):
        raise ValueError('native input requires a finite absolute deadline')
    def require_time():
        if deadline is not None and time.monotonic() >= deadline:
            raise CaseUnavailable('fixed-VM-input-operation-deadline')
    require_time()
    sequence = protocol.steps(operation)
    mailbox = mailbox or Mailbox(execution, vm_id)
    mailbox.attach()
    transaction = secrets.token_hex(16)
    events = []
    pending, delivery, published = None, None, False
    try:
        for ordinal, _ in enumerate(sequence):
            pending, delivery, published = None, None, False
            require_time()
            before = actor.observe()
            require_time()
            pending = protocol.request({'format_version': 1, 'binding': execution,
                'transaction_id': transaction, 'operation': operation,
                'ordinal': ordinal, 'focus': before}, execution)
            require_time()
            mailbox.write('request.json', pending)
            published = True
            step_deadline = time.monotonic() + 10
            if deadline is not None:
                step_deadline = min(step_deadline, deadline)
            while True:
                acknowledgment = mailbox.read('ack.json')
                if acknowledgment is not None:
                    delivery = protocol.acknowledgment(acknowledgment, pending, vm_id)
                    break
                actor.observe()
                if time.monotonic() >= step_deadline:
                    raise CaseUnavailable('fixed-VM-input-acknowledgment-unavailable')
                time.sleep(0.05)
            if ordinal == len(sequence) - 1:
                # Let the final Wayland key event drain to WebKit before focus moves.
                if deadline is not None and time.monotonic() + 0.2 >= deadline:
                    raise CaseUnavailable('fixed-VM-input-operation-deadline')
                time.sleep(0.2)
            require_time()
            after = actor.observe()
            require_time()
            if after != before:
                raise ValueError('native input focus identity changed across a physical input step')
            events.append({'request': pending, 'delivery': delivery, 'focus_after': after})
            mailbox.clear_completed(pending)
    except BaseException as error:
        error.observations = {**getattr(error, 'observations', {}), 'native_input': {
            'format_version': 1, 'vm_id': vm_id, 'transaction_id': transaction, 'operation': operation,
            'completed': False, 'events': events,
            **({'pending_request': pending, 'request_published': published,
                'validated_delivery': delivery} if deadline is not None and pending is not None else {})}}
        raise
    return {'format_version': 1, 'vm_id': vm_id, 'transaction_id': transaction, 'operation': operation,
            'completed': True, 'events': events, 'text_readback': False}

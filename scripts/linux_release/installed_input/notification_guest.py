"""Consume one closed notification click without replaying an uncertain physical delivery."""
from __future__ import annotations

import secrets
import time

from . import notification_wire, protocol
from .mailbox import Mailbox
from ..installed_cases.records import CaseUnavailable


def perform(execution, vm_id, observer, mailbox=None):
    mailbox = mailbox or Mailbox(execution, vm_id)
    mailbox.attach()
    transaction, events = secrets.token_hex(16), []
    phase = 'initial-observation'
    try:
        initial = observer.observe()
        phase = 'activation-arm'
        observer.arm()
        phase = 'pinned-observation'
        before = observer.observe()
        notification_wire.same_target(initial, before)
        pending = protocol.request({'format_version': 1, 'binding': execution,
            'transaction_id': transaction, 'operation': notification_wire.OPERATION,
            'ordinal': 0, 'notification': before}, execution)
        phase = 'request-publication'
        mailbox.write('request.json', pending)
        phase = 'acknowledgment'
        deadline = time.monotonic() + 10
        while True:
            acknowledgment = mailbox.read('ack.json')
            if acknowledgment is not None:
                delivery = protocol.acknowledgment(acknowledgment, pending, vm_id)
                break
            observer.require_runtime()
            if time.monotonic() >= deadline:
                raise CaseUnavailable('fixed-notification-input-acknowledgment-unavailable')
            time.sleep(0.02)
        phase = 'post-click-settlement'
        try:
            after = observer.settle(before, time.monotonic() + 15)
        except BaseException as error:
            events.append({'request': pending, 'delivery': delivery,
                'notification_after': getattr(error, 'observations', {}).get('notification_after')})
            raise
        events.append({'request': pending, 'delivery': delivery, 'notification_after': after})
        phase = 'completed-exchange-cleanup'
        mailbox.clear_completed(pending)
    except BaseException as error:
        error.observations = {**getattr(error, 'observations', {}), 'notification_input_phase': phase, 'native_input': {
            'format_version': 1, 'vm_id': vm_id, 'transaction_id': transaction,
            'operation': notification_wire.OPERATION, 'completed': False, 'events': events}}
        raise
    return {'format_version': 1, 'vm_id': vm_id, 'transaction_id': transaction,
        'operation': notification_wire.OPERATION, 'completed': True, 'events': events, 'text_readback': False}

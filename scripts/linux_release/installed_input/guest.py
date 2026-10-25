"""Request sealed physical input while continuously observing the owned UI focus."""
from __future__ import annotations

import secrets
import time

from . import protocol
from .mailbox import Mailbox
from ..installed_cases.records import CaseUnavailable


def perform(execution: dict, vm_id: str, actor, mailbox=None, *, operation='editor-replace-save-v1') -> dict:
    sequence = protocol.steps(operation)
    mailbox = mailbox or Mailbox(execution, vm_id)
    mailbox.attach()
    transaction = secrets.token_hex(16)
    events = []
    progress = {'ordinal': None, 'phase': 'before-sequence', 'request_publication_confirmed': False,
                'acknowledgment_validated': False}
    try:
        for ordinal, _ in enumerate(sequence):
            progress = {'ordinal': ordinal, 'phase': 'observe-before-request', 'request_publication_confirmed': False,
                        'acknowledgment_validated': False}
            before = actor.observe()
            progress['phase'] = 'build-request'
            pending = protocol.request({'format_version': 1, 'binding': execution,
                'transaction_id': transaction, 'operation': operation,
                'ordinal': ordinal, 'focus': before}, execution)
            progress['phase'] = 'publish-request'
            mailbox.write('request.json', pending)
            progress['request_publication_confirmed'] = True
            deadline = time.monotonic() + 10
            while True:
                progress['phase'] = 'read-acknowledgment'
                acknowledgment = mailbox.read('ack.json')
                if acknowledgment is not None:
                    progress['phase'] = 'validate-acknowledgment'
                    delivery = protocol.acknowledgment(acknowledgment, pending, vm_id)
                    progress['acknowledgment_validated'] = True
                    break
                progress['phase'] = 'observe-awaiting-acknowledgment'
                actor.observe()
                if time.monotonic() >= deadline:
                    raise CaseUnavailable('fixed-VM-input-acknowledgment-unavailable')
                time.sleep(0.05)
            if ordinal == len(sequence) - 1:
                # Let the final Wayland key event drain to WebKit before focus moves.
                progress['phase'] = 'final-key-drain'
                time.sleep(0.2)
            progress['phase'] = 'observe-after-acknowledgment'
            after = actor.observe()
            progress['phase'] = 'compare-focus'
            if after != before:
                raise ValueError('native input focus identity changed across a physical input step')
            events.append({'request': pending, 'delivery': delivery, 'focus_after': after})
            progress['phase'] = 'clear-completed'
            mailbox.clear_completed(pending)
    except BaseException as error:
        error.observations = {**getattr(error, 'observations', {}), 'native_input': {
            'format_version': 1, 'vm_id': vm_id, 'transaction_id': transaction, 'operation': operation,
            'completed': False, 'events': events, 'failure_context': progress}}
        raise
    return {'format_version': 1, 'vm_id': vm_id, 'transaction_id': transaction, 'operation': operation,
            'completed': True, 'events': events, 'text_readback': False}

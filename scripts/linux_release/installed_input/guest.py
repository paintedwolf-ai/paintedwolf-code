"""Request sealed physical input while continuously observing the owned UI focus."""
from __future__ import annotations

import secrets
import time

from . import protocol
from .mailbox import Mailbox
from ..installed_cases.records import CaseUnavailable


def perform(execution: dict, vm_id: str, actor, mailbox=None) -> dict:
    mailbox = mailbox or Mailbox(execution, vm_id)
    mailbox.attach()
    transaction = secrets.token_hex(16)
    events = []
    try:
        for ordinal, _ in enumerate(protocol.steps('editor-replace-save-v1')):
            before = actor.observe()
            pending = protocol.request({'format_version': 1, 'binding': execution,
                'transaction_id': transaction, 'operation': 'editor-replace-save-v1',
                'ordinal': ordinal, 'focus': before}, execution)
            mailbox.write('request.json', pending)
            deadline = time.monotonic() + 10
            while True:
                acknowledgment = mailbox.read('ack.json')
                if acknowledgment is not None:
                    delivery = protocol.acknowledgment(acknowledgment, pending, vm_id)
                    break
                actor.observe()
                if time.monotonic() >= deadline:
                    raise CaseUnavailable('fixed-VM-input-acknowledgment-unavailable')
                time.sleep(0.05)
            after = actor.observe()
            if after != before:
                raise ValueError('native input focus identity changed across a physical input step')
            events.append({'request': pending, 'delivery': delivery, 'focus_after': after})
            mailbox.clear_completed(pending)
    except BaseException as error:
        error.observations = {**getattr(error, 'observations', {}), 'native_input': {
            'format_version': 1, 'vm_id': vm_id, 'transaction_id': transaction,
            'completed': False, 'events': events}}
        raise
    return {'format_version': 1, 'vm_id': vm_id, 'transaction_id': transaction,
            'completed': True, 'events': events, 'text_readback': False}

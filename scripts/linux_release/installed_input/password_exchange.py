"""Closed host-side private fixture exchange; only aggregate facts leave the channel."""
from __future__ import annotations

import copy

from . import password_wire
from .password_actor import Fixture


def observed(ordinal, pending):
    return {'operation': 'focused', 'ordinal': ordinal, 'target': copy.deepcopy(pending['target'])}


def exchange(actor, pending, pipe):
    """The caller owns the authenticated SSH pipe and its single fixed deadline."""
    fixture, value, selected = None, None, None
    try:
        password_wire.request(pending, pending['binding'])
        selected = copy.deepcopy(pending)
        if not 0 < pipe.remaining() <= password_wire.MAX_SECONDS:
            raise ValueError('private fixture transport has no bounded one-use lifetime')
        value = pipe.receive_fixture_value()
        fixture = Fixture(actor, pending, value)
        value = None
        with fixture:
            # Receipt of private bytes must not restart the transaction's clock.
            actor.deadline = min(actor.deadline, pipe.deadline)
            for ordinal in range(password_wire.SECRET_BYTES + 1):
                pipe.send_metadata({'operation': 'observe-before', 'ordinal': ordinal})
                if pipe.receive_metadata() != observed(ordinal, pending):
                    raise ValueError('private fixture lost its captured target before input')
                fixture.deliver(ordinal)
                pipe.send_metadata({'operation': 'observe-after', 'ordinal': ordinal})
                if pipe.receive_metadata() != observed(ordinal, pending):
                    raise ValueError('private fixture lost its captured target after input')
            delivery = fixture.finish()
            pipe.send_metadata({'operation': 'finished', 'delivery': delivery})
            if pipe.receive_metadata() != {'operation': 'accepted', 'transaction_id': pending['transaction_id']}:
                raise ValueError('private fixture has no matching guest completion')
            pipe.remaining()
            return {'request': copy.deepcopy(pending), 'delivery': delivery, 'guest_confirmed': True}
    except BaseException as error:
        actor.failed = True
        # Never serialize exception arguments or per-key intermediate frames.
        error.private_fixture = {'guest_confirmed': False, 'failure_type': type(error).__name__}
        if selected is not None:
            error.private_fixture['request'] = selected
        if fixture is not None:
            error.private_fixture['delivery'] = fixture.outcome()
        raise
    finally:
        value = None

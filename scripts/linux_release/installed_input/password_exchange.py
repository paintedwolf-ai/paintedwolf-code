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
        try:
            value = password_wire.private_secret(pipe.receive_frame(128).decode('ascii'))
        except (ValueError, UnicodeError):
            raise ValueError('private actor value has an invalid fixed shape') from None
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


def witness(channel, pending, private_value, focus, vm_id):
    """The case alone observes its target; the actor alone performs physical keys."""
    password_wire.request(pending, pending['binding'])
    try:
        channel.check()
        if focus.observe() != pending['target']:
            raise ValueError('private fixture changed target before its private handoff')
        channel.send_metadata({'request': pending})
        channel.send_fixture_value(private_value)
        private_value = None
        for ordinal in range(password_wire.SECRET_BYTES + 1):
            for operation in ('observe-before', 'observe-after'):
                if channel.receive_metadata() != {'operation': operation, 'ordinal': ordinal}:
                    raise ValueError('private fixture physical sequence changed or replayed')
                channel.check()
                if focus.observe() != pending['target']:
                    raise ValueError('private fixture target changed across physical input')
                channel.send_metadata(observed(ordinal, pending))
        finished = channel.receive_metadata()
        if not isinstance(finished, dict) or set(finished) != {'operation', 'delivery'} or finished['operation'] != 'finished':
            raise ValueError('private fixture omitted its bounded completion')
        delivery = password_wire.completed(finished['delivery'], pending, vm_id)
        channel.send_metadata({'operation': 'accepted', 'transaction_id': pending['transaction_id']})
        return {'request': copy.deepcopy(pending), 'delivery': delivery, 'guest_confirmed': True}
    finally:
        private_value = None


class Session:
    """Own three closed stages and their aggregate log, never the fixture value."""
    def __init__(self, actor, selected_actor, execution, output):
        import time
        self.actor, self.selected_actor = actor, password_wire.actor(selected_actor)
        self.execution, self.output = copy.deepcopy(execution), output
        self.stream, self.started, self.subject = None, None, None
        self.stage, self.bytes, self.failed = 0, 0, False
        self.clock = time.monotonic

    def _retain(self, facts):
        import os
        from . import protocol
        raw = protocol.canonical(facts)
        if self.bytes + len(raw) > len(password_wire.STAGES) * protocol.MAX_DOCUMENT:
            raise ValueError('private fixture aggregate log exceeded its fixed bound')
        if self.stream is None:
            descriptor = os.open(self.output / 'native-password-input.jsonl',
                                 os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            self.stream = os.fdopen(descriptor, 'wb')
        self.stream.write(raw)
        self.stream.flush()
        os.fsync(self.stream.fileno())
        self.bytes += len(raw)

    def perform(self, pending, pipe):
        if self.failed or self.stage >= len(password_wire.STAGES):
            self.failed, self.actor.failed = True, True
            return False
        selected, facts = None, None
        try:
            password_wire.request(pending, self.execution)
            selected = copy.deepcopy(pending)
            if (self.failed or self.stage >= len(password_wire.STAGES)
                    or pending['stage'] != password_wire.STAGES[self.stage]
                    or pending['actor'] != self.selected_actor):
                raise ValueError('private fixture stage changed or replayed its selected actor')
            subject = {key: pending[key] for key in ('actor', 'case', 'profile', 'session')}
            if self.started is None:
                self.started, self.subject = self.clock(), copy.deepcopy(subject)
            if self.clock() - self.started > 300 or subject != self.subject:
                raise ValueError('private fixture lifetime or owned profile changed')
            pipe.deadline = min(pipe.deadline, self.started + 300)
            if pipe.line({'operation': 'open-password-fixture', 'transaction_id': pending['transaction_id']}) != {'state': 'private-fixture-open'}:
                raise ValueError('private fixture SSH endpoint refused its fixed opening')
            facts = exchange(self.actor, pending, pipe)
            self._retain(facts)
            self.stage += 1
            return True
        except BaseException as error:
            # Copy only this closed exchange's aggregate; never retain the error object.
            private = getattr(error, 'private_fixture', None)
            facts = {'guest_confirmed': False, 'failure_type': type(error).__name__}
            if selected is not None:
                facts['request'] = selected
            if isinstance(private, dict) and 'delivery' in private:
                facts['delivery'] = copy.deepcopy(private['delivery'])
        self.failed, self.actor.failed = True, True
        try:
            self._retain(facts)
        except BaseException:
            pass
        return False

    def close(self):
        self.failed, self.actor.failed = True, True
        if self.stream is not None:
            self.stream.close()
            self.stream = None

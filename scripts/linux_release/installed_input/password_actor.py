"""A private closed fixture transaction shares the leased QMP actor without key logs."""
from __future__ import annotations

import copy
import time

from . import password_wire


class Fixture:
    def __init__(self, actor, pending, private_value):
        self.actor, self._value = actor, None
        try:
            password_wire.request(pending, pending['binding'])
            if pending['binding']['execution_nonce'] != actor.nonce:
                raise ValueError('private fixture differs from the captured physical actor execution')
            self.pending = copy.deepcopy(pending)
            self._value = password_wire.private_secret(private_value)
        except BaseException:
            actor.failed = True
            raise
        self.count, self.finished, self.entered, self.uncertain = 0, False, False, False

    def _refuse(self):
        self.actor.failed = True
        self._value = None
        raise ValueError('private fixture rejected input and cannot resume')

    def __enter__(self):
        self.actor.lock.acquire()
        try:
            if self.actor.failed or self.actor.closed or self.actor.deadline is not None:
                raise ValueError('physical actor is unavailable for its private fixture')
            self.actor.deadline = time.monotonic() + password_wire.MAX_SECONDS
            self.actor._reserve(password_wire.OPERATION + ':' + self.pending['stage'], 0,
                                password_wire.SECRET_BYTES + 1, self.pending['transaction_id'])
            self.actor.validate()
            self.entered = True
            return self
        except BaseException:
            self.actor.failed = True
            try:
                self._discard()
            finally:
                self.actor.lock.release()
            raise

    def deliver(self, ordinal):
        if (not self.entered or self.finished or self.actor.failed or self.actor.closed
                or type(ordinal) is not int or ordinal != self.count
                or not 0 <= ordinal <= password_wire.SECRET_BYTES):
            self._refuse()
        try:
            self.actor._reserve(password_wire.OPERATION + ':' + self.pending['stage'], ordinal,
                                password_wire.SECRET_BYTES + 1, self.pending['transaction_id'])
            if ordinal == 0:
                keys = ('ctrl', 'a')
            else:
                char = self._value[ordinal - 1]
                keys = ('shift', char.lower()) if char.isupper() else (char,)
            self.actor._send_keys(keys)
            self.count += 1
        except BaseException:
            self.uncertain = True
            self.actor.failed = True
            raise

    def finish(self):
        if (not self.entered or self.finished or self.actor.failed or self.actor.closed
                or self.uncertain or self.count != password_wire.SECRET_BYTES + 1):
            self._refuse()
        try:
            self.actor._bounded()
        except BaseException:
            self.actor.failed = True
            self._value = None
            raise
        self.finished = True
        return self.outcome()

    def outcome(self):
        return {'format_version': 1, 'execution_nonce': self.actor.nonce,
            'transaction_id': self.pending['transaction_id'], 'stage': self.pending['stage'],
            **{key: copy.deepcopy(self.pending[key]) for key in ('actor', 'case', 'profile', 'session', 'target')},
            'acknowledged_count': self.count, 'completed': self.finished,
            'delivery_uncertain': self.uncertain, 'lease': self.actor.identity}

    def _discard(self):
        # Lifetime is bounded by this context/channel; Python strings are not zeroized.
        self._value = None
        self.actor.deadline = None
        if self.actor.connection is not None:
            self.actor.connection.settimeout(3)

    def __exit__(self, kind, error, traceback):
        try:
            if error is not None or not self.finished:
                self.actor.failed = True
        finally:
            try:
                self._discard()
            finally:
                self.entered = False
                self.actor.lock.release()
        if error is None and not self.finished:
            raise ValueError('private fixture ended before its fixed physical sequence completed')

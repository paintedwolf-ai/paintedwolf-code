"""Request only the prepared OS fixture transaction over the captured guest mailbox."""
from __future__ import annotations

import os
from pathlib import Path
import pwd
import secrets
import time

from . import session_wire, unlock_wire
from .mailbox import Mailbox
from ..installed_cases.records import CaseUnavailable


class NativeUnlockControl:
    def __init__(self, execution, vm_id, lock):
        self.execution, self.vm_id, self.lock = execution, vm_id, lock
        account = pwd.getpwuid(os.getuid())
        self.account = {'name': account.pw_name, 'uid': account.pw_uid,
                        'gid': account.pw_gid, 'home': account.pw_dir}
        if Path.home() != Path(account.pw_dir):
            raise ValueError('OS fixture account differs from the selected physical home')
        self.mailbox = Mailbox(execution, vm_id)
        self.mailbox.attach()
        self.prepared, self.used = None, False

    def _request(self, operation):
        pending = unlock_wire.request({'format_version': 1, 'binding': self.execution,
            'transaction_id': secrets.token_hex(16), 'operation': operation,
            'native_session': self.lock.observe(), 'account': self.account}, self.execution)
        self.mailbox.write('request.json', pending)
        deadline = time.monotonic() + 40
        while True:
            ack = self.mailbox.read('ack.json')
            if ack is not None:
                unlock_wire.acknowledgment(ack, pending, self.vm_id)
                self.mailbox.clear_completed(pending)
                return {'request': pending, 'acknowledgment': ack}
            if time.monotonic() >= deadline:
                error = CaseUnavailable('native-unlock-custody-or-completion-unverified')
                error.observations = {'native_unlock_request': pending}
                raise error
            time.sleep(0.05)

    def prepare(self):
        if self.prepared is not None or self.used:
            raise ValueError('OS fixture credential preparation cannot replay')
        self.prepared = self._request(unlock_wire.PREPARE)
        return self.prepared

    def unlock(self):
        if self.prepared is None or self.used:
            raise ValueError('native unlock requires its one-use prepared custody')
        self.used = True
        result = self._request(unlock_wire.PURPOSE)
        final = self.lock.observe()
        session_wire.same_runtime(self.prepared['request']['native_session'], final)
        if any(final['native_session'][key] for key in ('locked_hint', 'locker_active')):
            raise CaseUnavailable('main-case-observer-did-not-independently-confirm-native-unlock')
        return {**result, 'independent_session_after': final}

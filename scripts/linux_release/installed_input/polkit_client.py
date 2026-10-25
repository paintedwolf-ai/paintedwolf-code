"""Case-side requests for one captured native call; public replies never authorize input."""
from __future__ import annotations

import os
from pathlib import Path
import pwd
import secrets
import time

from . import polkit_control, polkit_runtime, protocol
from .mailbox import Mailbox
from .unlock_account import validate_account_shape
from ..installed_cases.records import CaseUnavailable


class NativePolkitControl:
    def __init__(self, app, native, session, execution, vm_id):
        self.app, self.native, self.session = app, native, session
        self.execution, self.vm_id = protocol.binding(execution), vm_id
        account = pwd.getpwuid(os.getuid())
        self.account = validate_account_shape({'name': account.pw_name, 'uid': account.pw_uid,
            'gid': account.pw_gid, 'home': account.pw_dir})
        if Path.home() != Path(account.pw_dir):
            raise ValueError('Polkit QA account differs from the actual selected home')
        self.mailbox = Mailbox(execution, vm_id)
        self.mailbox.attach()
        self.session_id = secrets.token_hex(16)
        self.records = []
        self.started = self.closed = self.failed = False

    def _request(self, operation, **fields):
        if self.closed or self.failed:
            raise ValueError('Polkit client cannot resume an uncertain request')
        pending = polkit_control.request({'format_version': 1, 'binding': self.execution,
            'transaction_id': secrets.token_hex(16), 'session_id': self.session_id,
            'operation': operation, 'account': self.account, **fields}, self.execution)
        record = {'request': pending}
        self.records.append(record)
        self.mailbox.write('request.json', pending)
        deadline = time.monotonic() + (65 if operation in {polkit_control.START, polkit_control.START_STALE} else 40)
        try:
            while True:
                self.app.require_running()
                self.app.require_engine()
                acknowledgment = self.mailbox.read('ack.json')
                if acknowledgment is not None:
                    polkit_control.acknowledgment(acknowledgment, pending, self.vm_id)
                    self.mailbox.clear_completed(pending)
                    record['acknowledgment'] = acknowledgment
                    return acknowledgment['result']
                if time.monotonic() >= deadline:
                    raise CaseUnavailable('native-polkit-control-completion-unverified')
                time.sleep(0.05)
        except BaseException:
            self.failed = True
            raise

    def start(self, *, stale=False):
        if self.started:
            raise ValueError('Polkit client cannot replay its initial capture')
        self.started = True
        return self._request(polkit_control.START_STALE if stale else polkit_control.START, native_session=self.session.observe(),
            runtime=polkit_runtime.selection(self.app, self.native))

    def bind(self, attempt):
        return self._request(polkit_control.BIND, attempt=attempt)['observation']

    def observe(self):
        return self._request(polkit_control.OBSERVE)['observation']

    def deliver(self, *, cancel=False):
        return self._request(polkit_control.CANCEL if cancel else polkit_control.PURPOSE)

    def close(self):
        if self.closed:
            return
        try:
            # A pending uncertain mailbox publication cannot be overwritten.
            # Its physical controller retains independent cleanup responsibility.
            if self.started and not self.failed:
                self._request(polkit_control.CLOSE)
        finally:
            self.closed = True

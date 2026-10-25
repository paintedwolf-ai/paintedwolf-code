"""One-use dedicated QA OS credential custody, separate from the read-only collector."""
from __future__ import annotations

from copy import deepcopy
import secrets

from . import protocol, unlock_wire
from .mailbox import decode
from .polkit_proxy import ACCOUNT_BOOTSTRAP
from .unlock_account import validate_account_shape
from ..installed_cases.native_command import command as bounded_command


class Credential:
    def __init__(self, actor, target, execution, account):
        self.actor, self.target = actor, deepcopy(target)
        self.execution = deepcopy(protocol.binding(execution))
        self.account = dict(validate_account_shape(account))
        if (actor.nonce != execution['execution_nonce']
                or actor.value['ssh'] != {key: target[key] for key in ('host', 'port', 'user')}):
            raise ValueError('Polkit OS credential differs from the physical actor guest lease')
        alphabet = b'0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz'
        self.secret = bytearray(secrets.choice(alphabet) for _ in range(40))
        self.attempted = self.prepared = self.claimed = self.closed = False

    def _command(self, action, frame=None):
        from ..installed_campaign import command
        if action not in {'prepare', 'disable'}:
            raise ValueError('Polkit QA credential requires its fixed prepare or disable action')
        arguments = ['/usr/bin/sudo', '-n', '/usr/bin/env', '-i', 'PATH=/usr/bin:/bin',
            'LANG=C.UTF-8', 'HOME=/root', '/usr/bin/python3', '-I', '-B', '-c', ACCOUNT_BOOTSTRAP,
            self.execution['driver_tree'], action, '--account', protocol.canonical(self.account).decode().strip(),
            '--vm-id', self.actor.value['vm_id']]
        code, stdout, _ = bounded_command(command(self.target, arguments), timeout=20, limit=4096, input_bytes=frame)
        if code != 0:
            raise ValueError('Polkit QA OS credential operation did not settle')
        result = decode(stdout)
        expected = ({'prepared': True, 'account': self.account, 'credential_state': 'active'} if action == 'prepare'
            else {'disabled': True, 'account': self.account, 'shadow_locked': True,
                  'credential_state': 'locked', 'sessions_terminated': False})
        if result != expected:
            raise ValueError('Polkit QA OS credential acknowledgment changed its closed identity')
        return result

    def prepare(self):
        if self.closed or self.attempted:
            raise ValueError('Polkit QA OS credential preparation cannot resume or replay')
        self.attempted = True
        frame = bytearray(unlock_wire.secret_frame(self.secret))
        try:
            result = self._command('prepare', frame)
            self.prepared = True
            return result
        finally:
            frame[:] = b'\0' * len(frame)

    def take(self):
        if self.closed or self.claimed or not self.prepared:
            raise ValueError('Polkit QA OS credential has no unused prepared custody')
        self.claimed = True
        try:
            return bytearray(self.secret)
        finally:
            self.secret[:] = b'\0' * len(self.secret)

    def close(self):
        if self.closed:
            return
        self.closed = True
        try:
            # Preparation uncertainty still consumes the attempt and requires a
            # bounded lock of only the same independently marked QA account.
            return self._command('disable') if self.attempted else None
        finally:
            self.secret[:] = b'\0' * len(self.secret)

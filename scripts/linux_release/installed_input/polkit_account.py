"""One-use dedicated QA OS credential custody, separate from the read-only collector."""
from __future__ import annotations

from copy import deepcopy
import secrets

from . import protocol, session_wire, unlock_wire
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
        from ..campaign.guests import command
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


class StaleCredential(Credential):
    """One account preparation, then one unlock and one original Polkit claim."""
    def __init__(self, actor, target, execution, account, native):
        super().__init__(actor, target, execution, account)
        self.native = deepcopy(session_wire.native_session(native))
        if (self.native['session']['User'] != str(self.account['uid'])
                or self.native['locked_hint'] or self.native['locker_active']):
            self.close()
            raise ValueError('stale credential requires its selected unlocked session')
        self.phase = 'prepared'
        self.attempt = None

    def bind(self, account, native):
        if self.closed or account != self.account:
            raise ValueError('stale credential replaced its marked account')
        session_wire.same_session(self.native, native)

    def pending(self, observation):
        from .polkit_wire import validate
        self.bind(self.account, observation['native_session'])
        attempt = validate(observation['attempt'])
        if (attempt['subject']['uid'] != self.account['uid']
                or attempt['phase'] != 'interactive' or attempt['transport'] != 'pending'
                or self.attempt is not None and self.attempt != attempt):
            raise ValueError('stale credential replaced its original system authentication call')
        self.attempt = deepcopy(attempt)

    def take(self):
        raise ValueError('stale credential requires its explicit ordered purpose')

    def unlock_claim(self, native):
        self.bind(self.account, native)
        if (not self.prepared or self.attempt is None or self.phase != 'prepared'
                or not all(native[key] for key in ('locked_hint', 'locker_active'))):
            raise ValueError('stale credential has no unused locked unlock claim')
        self.phase = 'unlock-claimed'
        return bytearray(self.secret)

    def unlocked(self, native):
        self.bind(self.account, native)
        if self.phase != 'unlock-claimed' or any(native[key] for key in ('locked_hint', 'locker_active')):
            raise ValueError('stale credential requires observed completion of its unlock')
        self.phase = 'unlocked'

    def polkit_claim(self, observation):
        self.pending(observation)
        native = observation['native_session']
        self.bind(self.account, native)
        if self.phase != 'unlocked' or any(native[key] for key in ('locked_hint', 'locker_active')):
            raise ValueError('stale credential has no original post-unlock Polkit claim')
        self.phase = 'polkit-claimed'
        try:
            return bytearray(self.secret)
        finally:
            self.secret[:] = b'\0' * len(self.secret)

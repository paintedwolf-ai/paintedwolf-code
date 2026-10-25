"""One-use OS credential delivery to the causally bound native Polkit agent.

The owning controller must attach its live root observer to the private endpoint
before using this stage. Public JSON observations never establish that custody.
"""
from __future__ import annotations

from . import accessibility, native_password_surface, protocol, session_wire, unlock_wire
from .polkit_wire import validate
from .private_pipe import Pipe
from .unlock_account import validate_account_shape

PURPOSE = 'native-polkit-qa-authenticate-v1'
CANCEL = 'native-polkit-qa-cancel-v1'


def target(value):
    fields = {'session', 'pending', 'window', 'modal', 'modal_state', 'password', 'ancestors',
              'window_state', 'password_state'}
    if not isinstance(value, dict) or set(value) != fields:
        raise ValueError('Polkit input requires its closed native target shape')
    native = session_wire.native_session(value['session'])
    if native['locked_hint'] or native['locker_active']:
        raise ValueError('Polkit input requires the captured unlocked graphical session')
    pending = value['pending']
    if not isinstance(pending, dict) or set(pending) != {
            'attempt', 'agent', 'check_serial', 'begin_serial', 'native_session'}:
        raise ValueError('Polkit input has no exact live transaction observation')
    root_session = session_wire.native_session(pending['native_session'])
    session_wire.same_session(native, root_session)
    if root_session['locked_hint'] or root_session['locker_active']:
        raise ValueError('Polkit input requires the independently root-observed session to be unlocked')
    attempt = validate(pending['attempt'])
    if attempt['phase'] != 'interactive' or attempt['transport'] != 'pending':
        raise ValueError('Polkit input requires the actual pending interactive check')
    shell = native['authorities']['shell']
    agent = pending['agent']
    if (not isinstance(agent, dict) or set(agent) != {'unique', 'process', 'path'}
            or not isinstance(agent['path'], str) or not agent['path'].startswith('/')
            or len(agent['path']) > 256 or agent['process']['pid'] != shell['pid']
            or agent['process']['start_time_ticks'] != shell['start_time_ticks']
            or agent['process']['uid'] != int(native['session']['User'])
            or attempt['subject']['uid'] != agent['process']['uid']
            or any(type(pending[key]) is not int or pending[key] <= 0
                   for key in ('check_serial', 'begin_serial'))):
        raise ValueError('Polkit input agent differs from the independently captured GNOME process')
    native_password_surface.validate(
        {key: value[key] for key in native_password_surface.FIELDS}, shell)
    if value['password'] is None:
        raise ValueError('Polkit input requires its actual focused password object')
    accessibility.validate_identity(value['modal'])
    state = value['modal_state']
    if (value['modal'] not in value['ancestors'][1:]
            or not isinstance(state, dict) or set(state) != {
                'focused', 'visible', 'enabled', 'sensitive', 'defunct', 'modal'}
            or any(type(item) is not bool for item in state.values()) or state['defunct']
            or not all(state[key] for key in ('modal', 'visible', 'enabled', 'sensitive'))):
        raise ValueError('Polkit password has no single live native modal ancestor')
    return value


def same_target(before, after):
    target(before)
    target(after)
    session_wire.same_session(before['session'], after['session'])
    if any(before[key] != after[key] for key in ('pending', 'window', 'modal', 'password', 'ancestors')):
        raise ValueError('Polkit input replaced its captured transaction or password object')


class Stage:
    def __init__(self, actor, execution, account, credential, *, observer, purpose=PURPOSE):
        self.actor, self.execution = actor, protocol.binding(execution)
        if self.execution['execution_nonce'] != actor.nonce:
            raise ValueError('Polkit input execution differs from the captured physical actor')
        self.account = dict(validate_account_shape(account))
        if purpose == PURPOSE:
            self.secret = bytearray(unlock_wire.secret(credential))
        elif purpose == CANCEL and credential is None:
            self.secret = bytearray()
        else:
            raise ValueError('Polkit input requires its fixed authenticate or cancel operation')
        self.purpose = purpose
        self.observer = observer
        self.used = False

    def close(self):
        self.secret[:] = b'\0' * len(self.secret)

    def perform(self, process, selector, deadline, transaction_id):
        try:
            return self._perform(Pipe(process, selector, deadline), protocol.nonce(transaction_id))
        finally:
            self.close()

    def _perform(self, pipe, transaction_id):
        actor = self.actor
        with actor.lock:
            if self.used or actor.failed or actor.closed or (actor.current is not None and
                    actor.ordinal != (actor.length or len(protocol.steps(actor.operation)))):
                raise ValueError('Polkit input cannot overlap, resume or replay physical delivery')
            self.used = True
            if transaction_id in actor.transactions or len(actor.transactions) >= 128:
                raise ValueError('Polkit input exhausted the physical actor transaction bound')
            actor.transactions.add(transaction_id)
            envelope = {'purpose': self.purpose, 'binding': self.execution, 'transaction_id': transaction_id}
            original = None
            try:
                for ordinal in range(41 if self.purpose == PURPOSE else 1):
                    # Live root observation is supplied by the owning controller,
                    # independently of the guest's AT-SPI target JSON.
                    pending = self.observer.observe()
                    if pending is None or 'host_release_verified' in pending:
                        raise ValueError('Polkit input has no live root-observed pending transaction')
                    pipe.send_metadata({**envelope, 'ordinal': ordinal, 'phase': 'observe', 'pending': pending})
                    reply = pipe.receive_metadata()
                    if (not isinstance(reply, dict) or set(reply) != {*envelope, 'ordinal', 'target'}
                            or any(reply[key] != item for key, item in envelope.items())
                            or type(reply['ordinal']) is not int or reply['ordinal'] != ordinal):
                        raise ValueError('Polkit private target exchange changed its transaction')
                    current = target(reply['target'])
                    if (current['pending'] != pending or
                            pending['attempt']['subject']['uid'] != self.account['uid']):
                        raise ValueError('Polkit native target differs from the independent root observer')
                    if original is None:
                        original = current
                    else:
                        same_target(original, current)
                    # Revalidate the root transaction after the guest read and
                    # immediately before every irreversible physical event.
                    if self.observer.observe() != pending:
                        raise ValueError('Polkit transaction changed before private delivery')
                    pipe.remaining()
                    if self.purpose == CANCEL:
                        keys = ('esc',)
                    elif ordinal == 40:
                        keys = ('ret',)
                    else:
                        byte = self.secret[ordinal]
                        keys = ('shift', chr(byte + 32)) if 65 <= byte <= 90 else (chr(byte),)
                    actor._deliver(keys)
                pipe.send_metadata({**envelope, 'phase': 'complete'})
                if pipe.receive_metadata() != {**envelope, 'phase': 'delivered'}:
                    raise ValueError('Polkit endpoint did not settle its exact private delivery')
                return {'purpose': self.purpose, 'transaction_id': transaction_id,
                        'delivered': True, 'text_readback': False, 'host_release_verified': False}
            except BaseException:
                actor.failed = True
                raise

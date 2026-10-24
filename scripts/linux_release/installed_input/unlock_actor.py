"""One private OS-unlock transaction on the already leased physical input actor."""
from __future__ import annotations

from . import protocol, session_wire, unlock_wire
from .private_pipe import Pipe


class NativeUnlockActor:
    def __init__(self, actor, execution, account, credential):
        self.actor, self.execution = actor, protocol.binding(execution)
        if self.execution['execution_nonce'] != actor.nonce:
            raise ValueError('native unlock execution differs from its captured physical actor')
        unlock_wire.secret(credential)
        self.account = dict(account)
        self.secret = bytearray(credential)
        self.used = False

    def close(self):
        self.secret[:] = b'\0' * len(self.secret)

    @staticmethod
    def _keys(byte):
        if 65 <= byte <= 90:
            return ('shift', chr(byte + 32))
        return (chr(byte),)

    def perform(self, process, selector, deadline, pending):
        try:
            return self._perform(Pipe(process, selector, deadline), pending)
        finally:
            self.close()

    def _perform(self, pipe, pending):
        unlock_wire.request(pending, self.execution)
        if pending['operation'] != unlock_wire.PURPOSE or pending['account'] != self.account:
            raise ValueError('native unlock differs from its prepared account and phase')
        actor = self.actor
        with actor.lock:
            if self.used or actor.failed or actor.closed or (actor.current is not None
                    and actor.ordinal != (actor.length or len(protocol.steps(actor.operation)))):
                raise ValueError('native unlock cannot overlap, resume or replay physical input')
            self.used = True
            transaction = pending['transaction_id']
            if transaction in actor.transactions or len(actor.transactions) >= 128:
                raise ValueError('native unlock exhausted the actor transaction bound')
            actor.transactions.add(transaction)
            envelope = {'purpose': unlock_wire.PURPOSE, 'binding': self.execution,
                        'transaction_id': transaction}
            original = None
            try:
                pipe.send_metadata({**envelope, 'phase': 'prompt'})
                prompt = pipe.receive_metadata()
                if (not isinstance(prompt, dict) or set(prompt) != {*envelope, 'target'}
                        or any(prompt[key] != value for key, value in envelope.items())):
                    raise ValueError('native unlock prompt exchange changed its transaction')
                initial = unlock_wire.target(prompt['target'])
                session_wire.same_runtime(pending['native_session'], initial['session'])
                unlock_wire.locked(initial, password=initial['password'] is not None)
                if initial['password'] is None:
                    pipe.remaining()
                    actor._deliver(('ret',))
                pipe.send_metadata({**envelope, 'phase': 'prompt-ready'})
                # The endpoint supplies a fresh native target before every delivery.
                # Secret bytes and resulting qcodes never enter public delivery logs.
                for ordinal in range(41):
                    pipe.send_metadata({**envelope, 'ordinal': ordinal, 'phase': 'observe'})
                    reply = pipe.receive_metadata()
                    if (not isinstance(reply, dict) or set(reply) != {*envelope, 'ordinal', 'target'}
                            or any(reply[key] != value for key, value in envelope.items())
                            or type(reply['ordinal']) is not int or reply['ordinal'] != ordinal):
                        raise ValueError('native unlock private target exchange changed its transaction')
                    current = unlock_wire.locked(reply['target'], password=True)
                    unlock_wire.same_runtime(initial, current)
                    if original is None:
                        original = current
                    else:
                        unlock_wire.same_runtime(original, current)
                        if current['password'] != original['password'] or current['ancestors'] != original['ancestors']:
                            raise ValueError('native unlock replaced its focused password object')
                    pipe.remaining()
                    actor._deliver(self._keys(self.secret[ordinal]) if ordinal < 40 else ('ret',))
                pipe.send_metadata({**envelope, 'phase': 'complete'})
                result = pipe.receive_metadata()
                if result != {**envelope, 'phase': 'delivered'}:
                    raise ValueError('native unlock endpoint did not settle its exact delivery')
                final = pipe.receive_metadata()
                if not isinstance(final, dict) or set(final) != {*envelope, 'phase', 'session_after'} or any(
                        final[key] != value for key, value in envelope.items()) or final['phase'] != 'unlocked':
                    raise ValueError('native unlock has no independent final session proof')
                result = {'purpose': unlock_wire.PURPOSE, 'transaction_id': transaction,
                          'delivered': True, 'text_readback': False, 'session_after': final['session_after']}
                unlock_wire.acknowledgment({'format_version': 1, 'request_sha256': unlock_wire.digest(pending),
                    'vm_id': actor.value['vm_id'], 'result': result}, pending, actor.value['vm_id'])
                return result
            except BaseException:
                actor.failed = True
                raise

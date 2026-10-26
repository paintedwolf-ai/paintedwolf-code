"""Two closed recovery phase requests; observations never confer root authority."""
import secrets
import re
import math
import sys
import json
import threading
import time

from . import protocol
from .mailbox import Mailbox

ABSENT, RESTORE = 'temporary-custodian-absence-v1', 'restore-custodian-baseline-v1'
OPERATIONS = (ABSENT, RESTORE)
RESULTS = ({'phase': 'temporary_custodian_absence', 'typed_startup_outcome_required': True},
           {'phase': 'custodian_restored', 'metadata_continuity': True, 'contents_read': False})


def request(value, execution):
    if (not isinstance(value, dict) or set(value) != {'format_version', 'binding', 'transaction_id', 'operation'}
            or type(value['format_version']) is not int or value['format_version'] != 1
            or protocol.binding(value['binding']) != protocol.binding(execution)
            or not isinstance(value['transaction_id'], str)
            or not re.fullmatch(r'[0-9a-f]{32}', value['transaction_id'])
            or value['operation'] not in OPERATIONS):
        raise ValueError('recovery phase request differs from its closed schema')
    return value


class RecoveryMailbox(Mailbox):
    @staticmethod
    def _name(name):
        if name not in {'binding.json', 'recovery-request.json', 'recovery-ack.json'}:
            raise ValueError('recovery mailbox refuses caller-selected paths')

    def completed(self, pending):
        # Match exact request bytes before removing only this exchange's leaves.
        if self.read('recovery-request.json') != pending:
            raise ValueError('recovery phase request changed before completion')
        self._directory()
        (self.path / 'recovery-ack.json').unlink()
        (self.path / 'recovery-request.json').unlink()


class GuestPhases:
    def __init__(self, execution, vm_id, deadline):
        if type(deadline) not in (int, float) or not math.isfinite(deadline):
            raise ValueError('recovery requires a finite absolute deadline')
        self.execution, self.vm_id = protocol.binding(execution), vm_id
        self.mailbox = RecoveryMailbox(execution, vm_id)
        self.mailbox.attach()
        self.deadline, self.index, self.failed = deadline, 0, False
        self.records = []

    def phase(self, operation):
        if self.failed or self.index >= 2 or operation != OPERATIONS[self.index]:
            raise ValueError('recovery phase cannot be replayed or reordered')
        self.failed = True  # Any uncertainty consumes continuation, including publication.
        if time.monotonic() >= self.deadline:
            raise TimeoutError('recovery phase deadline expired before publication')
        pending = request({'format_version': 1, 'binding': self.execution,
            'transaction_id': secrets.token_hex(16), 'operation': operation}, self.execution)
        record = {'request': pending}
        self.records.append(record)
        self.mailbox.write('recovery-request.json', pending)
        while time.monotonic() < self.deadline:
            acknowledgment = self.mailbox.read('recovery-ack.json')
            if acknowledgment is not None:
                expected = {'format_version': 1, 'request': pending,
                            'vm_id': self.vm_id, 'result': RESULTS[self.index]}
                if protocol.canonical(acknowledgment) != protocol.canonical(expected):
                    raise ValueError('recovery phase acknowledgment differs')
                if time.monotonic() >= self.deadline:
                    raise TimeoutError('recovery acknowledgment arrived after its lifetime')
                self.mailbox.completed(pending)
                record['acknowledgment'] = acknowledgment
                self.index, self.failed = self.index + 1, False
                return acknowledgment['result']
            time.sleep(0.05)
        raise TimeoutError('recovery phase completion unverified')


class RootPipePhases:
    """Native-host owner forwards only the two fixed commands on its owned root pipe."""
    def __init__(self, process, read_line, execution, vm_id, deadline):
        if type(deadline) not in (int, float) or not math.isfinite(deadline):
            raise ValueError('root recovery requires a finite absolute deadline')
        self.process, self.read_line = process, read_line
        self.execution, self.vm_id = protocol.binding(execution), vm_id
        self.deadline, self.index, self.failed = deadline, 0, False
        self.cancelled = threading.Event()

    def cancel(self):
        self.cancelled.set()

    def exchange(self, document):
        if self.failed or self.index >= 2:
            raise ValueError('root recovery phase cannot resume or replay')
        self.failed = True
        pending = request(document, self.execution)
        if pending['operation'] != OPERATIONS[self.index]:
            raise ValueError('root recovery phase cannot reorder')
        if self.cancelled.is_set() or time.monotonic() >= self.deadline:
            raise TimeoutError('root recovery phase deadline expired or cancelled')
        command = (b'custodian-absent\n', b'custodian-restore\n')[self.index]
        self.process.stdin.write(command)
        self.process.stdin.flush()
        result = self.read_line(self.deadline, self.cancelled)
        if self.cancelled.is_set() or time.monotonic() >= self.deadline:
            raise TimeoutError('root recovery phase response arrived after its lifetime')
        if protocol.canonical(result) != protocol.canonical(RESULTS[self.index]):
            raise ValueError('root recovery phase result differs')
        self.index, self.failed = self.index + 1, False
        return {'format_version': 1, 'request': pending, 'vm_id': self.vm_id, 'result': result}


def guest_exchange():
    """Unprivileged SSH helper: only this execution's two nonsecret phase leaves."""
    from .mailbox import decode
    value = decode(sys.stdin.buffer.read(65537))
    allowed = {'action', 'execution', 'vm_id'}
    if value.get('action') == 'acknowledge': allowed.add('document')
    if set(value) != allowed or value.get('action') not in {'read', 'acknowledge'}:
        raise ValueError('unknown recovery exchange')
    box = RecoveryMailbox(value['execution'], value['vm_id'])
    box.marker()
    # The native campaign publishes its nonce before the guest creates its mailbox.
    # Match the normal mailbox poll's waiting state; only reads may await publication.
    if value['action'] == 'read' and ((not box.path.exists() and not box.path.is_symlink())
            or box.read('binding.json') is None):
        print(json.dumps({'request': None}), flush=True)
        return
    box.attach()
    pending = box.read('recovery-request.json')
    if pending is not None: request(pending, value['execution'])
    if value['action'] == 'read':
        result = {'request': pending}
    else:
        ack = value['document']
        if (pending is None or not isinstance(ack, dict)
                or set(ack) != {'format_version', 'request', 'vm_id', 'result'}
                or type(ack['format_version']) is not int or ack['format_version'] != 1
                or ack['request'] != pending or ack['vm_id'] != value['vm_id']
                or protocol.canonical(ack['result']) != protocol.canonical(
                    RESULTS[OPERATIONS.index(pending['operation'])])):
            raise ValueError('recovery acknowledgment lost its exact request')
        box.write('recovery-ack.json', ack)
        result = {'acknowledged': True}
    print(json.dumps(result), flush=True)

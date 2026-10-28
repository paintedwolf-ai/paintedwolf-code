"""Bridge one selected guest mailbox to one captured VM's fixed input actor."""
from __future__ import annotations

import os
from pathlib import Path
import selectors
import subprocess
import threading
import time

from . import protocol
from .mailbox import decode
from .qemu import open_lease

MAX_LOG = 8 << 20


def run_managed(bridge, command, output):
    with output.open('xb') as log:
        process = subprocess.Popen(command, stdout=log, stderr=subprocess.STDOUT)
        try:
            while process.poll() is None:
                if bridge is not None:
                    bridge.require_healthy()
                time.sleep(0.1)
        except BaseException:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
            raise
    return process.returncode


def verify_deliveries(directory, execution, selected):
    selected = adapter(selected)
    path = directory / 'native-input.jsonl'
    leaf = directory / 'leaf/evidence/files/edit-search-review/execution.json'
    if selected is None:
        if path.exists():
            raise ValueError('unselected native input controller evidence is forbidden')
        return
    if path.stat().st_size > MAX_LOG:
        raise ValueError('native input delivery log exceeds its bound')
    rows = [decode(raw) for raw in path.read_bytes().splitlines()]
    records = []
    if leaf.exists():
        if leaf.stat().st_size > 1 << 20:
            raise ValueError('native input guest observations exceed their case bound')
        import json
        observations = json.loads(leaf.read_bytes())['observations']
        for key in ('editor_keyboard', 'editor_search_keyboard'):
            value = observations.get(key, {}).get('physical_vm_keyboard')
            if value is not None:
                records.append(value)
        if observations.get('native_input') is not None:
            records.append(observations['native_input'])
    operations = ('editor-replace-save-v1', 'editor-find-needle-v1')
    if len(records) > len(operations) or any(not isinstance(record, dict) for record in records):
        raise ValueError('native input has unexpected fixed transactions')
    if sum(len(record.get('events', [])) for record in records) != len(rows):
        raise ValueError('native input host and guest delivery counts differ')
    seen, offset = set(), 0
    for index, record in enumerate(records):
        operation = operations[index]
        events = record.get('events')
        if (record.get('operation') != operation or record.get('vm_id') != selected['vm_id']
                or type(record.get('format_version')) is not int or record['format_version'] != 1
                or type(record.get('completed')) is not bool or not isinstance(events, list)
                or record.get('text_readback', False) is not False
                or len(events) > len(protocol.steps(operation))
                or record['completed'] and len(events) != len(protocol.steps(operation))
                or not record['completed'] and index != len(records) - 1):
            raise ValueError('native input completion differs from its fixed physical sequence')
        transaction = protocol.nonce(record.get('transaction_id'))
        if transaction in seen:
            raise ValueError('native input retained transactions overlap or replay')
        seen.add(transaction)
        for ordinal, event in enumerate(events):
            row = rows[offset]
            offset += 1
            if set(row) != {'request', 'acknowledgment'} or set(event) != {'request', 'delivery', 'focus_after'}:
                raise ValueError('native input retained delivery has an unknown shape')
            pending = protocol.request(row['request'], execution)
            delivered = protocol.acknowledgment(row['acknowledgment'], pending, selected['vm_id'])
            if (pending['transaction_id'] != transaction or pending['operation'] != operation
                    or pending['ordinal'] != ordinal
                    or event != {'request': pending, 'delivery': delivered, 'focus_after': pending['focus']}):
                raise ValueError('native input host and guest focus or physical delivery differ')


def adapter(value):
    if value is None:
        return None
    if (not isinstance(value, dict) or set(value) != {'format_version', 'kind', 'lease', 'vm_id'}
            or type(value['format_version']) is not int or value['format_version'] != 1
            or value['kind'] != 'qemu-fixed-input' or not isinstance(value['lease'], str)
            or not Path(value['lease']).is_absolute() or '\0' in value['lease'] or '\n' in value['lease']
            or not isinstance(value['vm_id'], str) or not protocol.IDENTIFIER.fullmatch(value['vm_id'])):
        raise ValueError('installed input requires an exact selected root VM lease')
    return value


class Bridge:
    def __init__(self, selected, execution, ssh_command, output):
        self.selected = adapter(selected)
        self.execution = protocol.binding(execution)
        self.ssh_command, self.output = ssh_command, output
        self.stop = threading.Event()
        self.error, self.bytes = None, 0

    def __enter__(self):
        self.lease = open_lease(Path(self.selected['lease']), execution_nonce=self.execution['execution_nonce'],
                               vm_id=self.selected['vm_id'])
        self.actor = self.lease.__enter__()
        try:
            self.log = (self.output / 'native-input.jsonl').open('xb')
            self.errors = (self.output / 'native-input-transport.log').open('xb')
            self.process = subprocess.Popen(self.ssh_command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                            stderr=self.errors, start_new_session=True)
            self.selector = selectors.DefaultSelector()
            self.selector.register(self.process.stdout, selectors.EVENT_READ)
            self.thread = threading.Thread(target=self._run, name='fixed-native-input', daemon=True)
            self.thread.start()
        except BaseException:
            self.lease.__exit__(None, None, None)
            raise
        return self

    def exchange(self, document):
        self.process.stdin.write(protocol.canonical(document))
        self.process.stdin.flush()
        raw = bytearray()
        deadline = time.monotonic() + 5
        while len(raw) <= protocol.MAX_DOCUMENT:
            remaining = deadline - time.monotonic()
            if remaining <= 0 or not self.selector.select(remaining):
                raise ValueError('fixed native input guest transport exceeded its bounded response time')
            char = os.read(self.process.stdout.fileno(), 1)
            if not char:
                raise ValueError('fixed native input guest transport ended before its response')
            raw.extend(char)
            if char == b'\n':
                return decode(bytes(raw))
        raise ValueError('fixed native input guest transport exceeded its response bound')

    def _run(self):
        try:
            while not self.stop.is_set():
                row = self.exchange({'operation': 'poll'})
                if row in ({'state': 'waiting'}, {'state': 'acknowledged'}):
                    self.stop.wait(0.025)
                    continue
                if set(row) != {'state', 'request'} or row['state'] != 'request':
                    raise ValueError('fixed native input guest returned an unknown response')
                pending = protocol.request(row['request'], self.execution)
                # Reserve the maximum acknowledgment before any irreversible key delivery.
                if self.bytes + protocol.MAX_DOCUMENT > MAX_LOG:
                    raise ValueError('fixed native input controller log is full before delivery')
                delivered = self.actor.send(pending['operation'], pending['ordinal'],
                                            transaction_id=pending['transaction_id'])
                acknowledgment = {'format_version': 1, 'request_sha256': protocol.request_sha256(pending),
                                  'delivery': delivered}
                protocol.acknowledgment(acknowledgment, pending, self.selected['vm_id'])
                raw = protocol.canonical({'request': pending, 'acknowledgment': acknowledgment})
                self.log.write(raw)
                self.log.flush()
                os.fsync(self.log.fileno())
                self.bytes += len(raw)
                if self.exchange({'operation': 'ack', 'acknowledgment': acknowledgment}) != {'state': 'acknowledged'}:
                    raise ValueError('fixed native input guest refused its actual delivery acknowledgment')
        except BaseException as error:
            self.error = error
            self.stop.set()

    def require_healthy(self):
        if self.error is not None:
            raise ValueError('fixed native input bridge failed; retained host delivery log requires review') from self.error

    def __exit__(self, *error):
        self.stop.set()
        self.thread.join(6)
        if self.process.poll() is None:
            self.process.terminate()
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=5)
        self.thread.join(1)
        self.selector.close()
        self.process.stdin.close()
        self.process.stdout.close()
        self.log.close()
        self.errors.close()
        self.lease.__exit__(*error)
        if not any(error):
            self.require_healthy()

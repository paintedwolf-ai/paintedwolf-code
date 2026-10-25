"""Bridge one selected guest mailbox to one captured VM's fixed input actor."""
from __future__ import annotations

import os
import hashlib
import re
from pathlib import Path
import selectors
import subprocess
import threading
import time

from . import protocol, surface_wire, notification_wire
from ..installed_cases.records import expand_physical_transactions
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


def delivery_records(directory, execution, vm_id):
    import json
    records, operations = [], []
    cases = [('conversation/restart-and-reconnect', 'reply'), ('files/edit-search-review', 'editor'),
             ('windowing/workspace-activation-and-exit', 'workspace'),
             ('notifications/activation-token-and-chat', 'notification')]
    for case, kind in cases:
        leaf = directory / ('leaf/evidence/' + case + '/execution.json')
        if not leaf.exists():
            continue
        if leaf.stat().st_size > 1 << 20:
            raise ValueError('native input guest observations exceed their case bound')
        document = json.loads(leaf.read_bytes())
        observations = document['observations']
        if kind in {'reply', 'workspace', 'notification'}:
            launch = 'launch_before' if kind == 'reply' else 'launch'
            selected = observations.get(launch, {}).get('fixed_input', [])
            recovery = observations.get('provider_recovery')
            input_limit = 1 if recovery is not None else 4
            if not isinstance(selected, list) or len(selected) > input_limit:
                raise ValueError('reply input exceeds its fixed transaction count')
            partial = observations.get('native_input')
            provider_partial = (kind in {'workspace', 'notification'} and partial is not None
                and len(selected) < input_limit and not observations.get('transitions'))
            if partial is not None and (kind == 'reply' or provider_partial):
                selected = [*selected, partial]
            if len(selected) > input_limit:
                raise ValueError('reply input exceeds its fixed transaction count')
            selected = expand_physical_transactions(leaf, selected)
            order = ('provider-name-v1', None, 'provider-fixture-credential-v1', 'reply-fixture-prompt-v1')
            if recovery is not None:
                from ..installed_cases.provider_recovery import registered
                prior = leaf.with_name('provider-registration.bin')
                if prior.stat().st_size > 1 << 20:
                    raise ValueError('provider registration recovery exceeds its retained bound')
                raw = prior.read_bytes()
                binding = {'artifact': document['artifact'], 'source_commit': execution['source_commit'],
                           'native_input': {'binding': execution, 'vm_id': vm_id}}
                account = observations.get(launch, {}).get('session', {}).get('User')
                admitted = registered(json.loads(raw), binding, account, prior)
                if recovery != {**admitted, 'prior_execution_sha256': hashlib.sha256(raw).hexdigest()} or len(selected) > 1:
                    raise ValueError('provider recovery differs from its retained registration and fixed input order')
                order = ('reply-fixture-prompt-v1',)
            for index, record in enumerate(selected):
                operation = order[index]
                if operation is None:
                    operation = record.get('operation') if isinstance(record, dict) else None
                    if not isinstance(operation, str) or not re.fullmatch(r'provider-endpoint-v1-[0-9]{4,5}', operation):
                        raise ValueError('reply input has no fixed loopback endpoint operation')
                    protocol.steps(operation)
                operations.append(operation)
                records.append(record)
            if kind in {'workspace', 'notification'}:
                transitions = observations.get('transitions', [])
                if not isinstance(transitions, list):
                    raise ValueError('workspace input requires its fixed transition list')
                notification_partial = (kind == 'notification' and partial is not None
                    and partial.get('operation') == notification_wire.OPERATION)
                if partial is not None and not provider_partial and not notification_partial:
                    transitions = [*transitions, partial]
                expected = ('workspace-open-peer-v1', 'workspace-hide-main-v1',
                    'workspace-request-quit-v1', 'workspace-cancel-quit-v1',
                    'workspace-request-quit-v1', 'workspace-focus-quit-v1', 'workspace-approve-quit-v1')
                if kind == 'notification':
                    expected = expected[:2]
                if len(transitions) > len(expected) or transitions and len(selected) != input_limit:
                    raise ValueError('workspace input exceeds or precedes its declared physical sequence')
                transitions = expand_physical_transactions(leaf, transitions)
                records.extend(transitions)
                operations.extend(expected[:len(transitions)])
                if kind == 'notification':
                    click = observations.get('notification_click')
                    clicks = [] if click is None else [click]
                    if notification_partial:
                        clicks.append(partial)
                    if len(clicks) > 1 or clicks and (len(transitions) != 2 or len(selected) != input_limit):
                        raise ValueError('notification click repeats or precedes its declared background transition')
                    records.extend(expand_physical_transactions(leaf, clicks))
                    operations.extend([notification_wire.OPERATION] * len(clicks))
        else:
            selected = []
            for key in ('editor_keyboard', 'editor_search_keyboard'):
                value = observations.get(key, {}).get('physical_vm_keyboard')
                if value is not None:
                    selected.append(value)
            if observations.get('native_input') is not None:
                selected.append(observations['native_input'])
            if len(selected) > 2:
                raise ValueError('editor input exceeds its fixed transaction count')
            selected = expand_physical_transactions(leaf, selected)
            records.extend(selected)
            operations.extend(('editor-replace-save-v1', 'editor-find-needle-v1')[:len(selected)])
    return records, operations


def verify_deliveries(directory, execution, selected):
    selected = adapter(selected)
    path = directory / 'native-input.jsonl'
    if selected is None:
        if path.exists():
            raise ValueError('unselected native input controller evidence is forbidden')
        return
    if path.stat().st_size > MAX_LOG:
        raise ValueError('native input delivery log exceeds its bound')
    rows = [decode(raw) for raw in path.read_bytes().splitlines()]
    records, operations = delivery_records(directory, execution, selected['vm_id'])
    if len(records) != len(operations) or any(not isinstance(record, dict) for record in records):
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
            surface = operation in surface_wire.OPERATIONS
            notification = operation == notification_wire.OPERATION
            after_key = 'notification_after' if notification else 'surface_after' if surface else 'focus_after'
            host_fields = {'request', 'acknowledgment', 'preflight'} if notification else {'request', 'acknowledgment'}
            if set(row) != host_fields or set(event) != {'request', 'delivery', after_key}:
                raise ValueError('native input retained delivery has an unknown shape')
            pending = protocol.request(row['request'], execution)
            delivered = protocol.acknowledgment(row['acknowledgment'], pending, selected['vm_id'])
            if (pending['transaction_id'] != transaction or pending['operation'] != operation
                    or pending['ordinal'] != ordinal
                    or event['request'] != pending or event['delivery'] != delivered):
                raise ValueError('native input host and guest focus or physical delivery differ')
            if notification:
                expected_preflight = {'request_sha256': protocol.request_sha256(pending),
                    'notification_sha256': hashlib.sha256(protocol.canonical(pending['notification'])).hexdigest()}
                if row['preflight'] != expected_preflight:
                    raise ValueError('notification delivery has no exact fresh pre-dispatch proof')
                finished = notification_wire.finished(pending['notification'], event['notification_after'])
                if record['completed'] and not finished:
                    raise ValueError('notification click has no observed selected-chat activation')
            elif surface:
                if operation != 'workspace-approve-quit-v1':
                    surface_wire.same_runtime(pending['surface'], event['surface_after'])
                if record['completed'] and ordinal == len(events) - 1 and not surface_wire.finished(
                        operation, events[0]['request']['surface'], event['surface_after']):
                    raise ValueError('native input has no observed declared surface transition')
            elif event['focus_after'] != pending['focus']:
                raise ValueError('native input host and guest focus differ')


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
                preflight, options = None, {}
                if pending['operation'] == notification_wire.OPERATION:
                    digest = protocol.request_sha256(pending)
                    fresh = self.exchange({'operation': 'verify-notification', 'request_sha256': digest})
                    if (not isinstance(fresh, dict) or set(fresh) != {'state', 'request_sha256', 'notification'}
                            or fresh['state'] != 'verified-notification' or fresh['request_sha256'] != digest):
                        raise ValueError('notification pre-dispatch acknowledgment differs from its pending request')
                    notification_wire.same_target(pending['notification'], fresh['notification'])
                    if fresh['notification'] != pending['notification']:
                        raise ValueError('notification pre-dispatch proof changed its pinned runtime context')
                    preflight = {'request_sha256': digest,
                        'notification_sha256': hashlib.sha256(protocol.canonical(fresh['notification'])).hexdigest()}
                    options['notification'] = fresh['notification']
                    def retainable(preview):
                        acknowledgment = {'format_version': 1, 'request_sha256': digest,
                            'delivery': preview}
                        protocol.acknowledgment(acknowledgment, pending, self.selected['vm_id'])
                        protocol.canonical({'request': pending, 'preflight': preflight,
                            'acknowledgment': acknowledgment})
                    options['before_effect'] = retainable
                delivered = self.actor.send(pending['operation'], pending['ordinal'],
                                            transaction_id=pending['transaction_id'], **options)
                acknowledgment = {'format_version': 1, 'request_sha256': protocol.request_sha256(pending),
                                  'delivery': delivered}
                protocol.acknowledgment(acknowledgment, pending, self.selected['vm_id'])
                retained = {'request': pending, 'acknowledgment': acknowledgment}
                if preflight is not None:
                    retained['preflight'] = preflight
                raw = protocol.canonical(retained)
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

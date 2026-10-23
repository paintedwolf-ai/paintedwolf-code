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

from . import protocol, surface_wire, password_wire
from .mailbox import decode
from .qemu import open_lease
from .password_pipe import Pipe

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
             ('vault/presence-cancel-and-expiry', 'presence')]
    for case, kind in cases:
        leaf = directory / ('leaf/evidence/' + case + '/execution.json')
        if not leaf.exists():
            continue
        if leaf.stat().st_size > 1 << 20:
            raise ValueError('native input guest observations exceed their case bound')
        document = json.loads(leaf.read_bytes())
        observations = document['observations']
        if kind == 'presence':
            from .presence_fixture import FIELDS
            selected = observations.get('launch', {}).get('fixed_input', [])
            if not isinstance(selected, list):
                raise ValueError('presence input requires its fixed transaction list')
            partial = observations.get('native_input')
            if partial is not None:
                selected = [*selected, partial]
            expected = ('provider-name-v1', None, 'provider-fixture-credential-v1',
                        *(operation for _, operation, _, _ in FIELDS))
            if len(selected) > len(expected):
                raise ValueError('presence input exceeds its declared physical sequence')
            for index, record in enumerate(selected):
                operation = expected[index]
                if operation is None:
                    operation = record.get('operation') if isinstance(record, dict) else None
                    if not isinstance(operation, str) or not re.fullmatch(r'provider-endpoint-v1-[0-9]{4,5}', operation):
                        raise ValueError('presence input has no fixed loopback endpoint operation')
                    protocol.steps(operation)
                records.append(record)
                operations.append(operation)
        elif kind in {'reply', 'workspace'}:

            launch = 'launch_before' if kind == 'reply' else 'launch'
            selected = observations.get(launch, {}).get('fixed_input', [])
            recovery = observations.get('provider_recovery')
            input_limit = 1 if recovery is not None else 4
            if not isinstance(selected, list) or len(selected) > input_limit:
                raise ValueError('reply input exceeds its fixed transaction count')
            partial = observations.get('native_input')
            provider_partial = (kind == 'workspace' and partial is not None
                and len(selected) < input_limit and not observations.get('transitions'))
            if partial is not None and (kind == 'reply' or provider_partial):
                selected = [*selected, partial]
            if len(selected) > input_limit:
                raise ValueError('reply input exceeds its fixed transaction count')
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
                admitted = registered(json.loads(raw), binding, account)
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
            if kind == 'workspace':
                transitions = observations.get('transitions', [])
                if not isinstance(transitions, list):
                    raise ValueError('workspace input requires its fixed transition list')
                if partial is not None and not provider_partial:
                    transitions = [*transitions, partial]
                expected = ('workspace-open-peer-v1', 'workspace-hide-main-v1',
                    'workspace-request-quit-v1', 'workspace-cancel-quit-v1',
                    'workspace-request-quit-v1', 'workspace-focus-quit-v1', 'workspace-approve-quit-v1')
                if len(transitions) > len(expected) or transitions and len(selected) != input_limit:
                    raise ValueError('workspace input exceeds or precedes its declared physical sequence')
                records.extend(transitions)
                operations.extend(expected[:len(transitions)])
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
            after_key = 'surface_after' if surface else 'focus_after'
            if set(row) != {'request', 'acknowledgment'} or set(event) != {'request', 'delivery', after_key}:
                raise ValueError('native input retained delivery has an unknown shape')
            pending = protocol.request(row['request'], execution)
            delivered = protocol.acknowledgment(row['acknowledgment'], pending, selected['vm_id'])
            if (pending['transaction_id'] != transaction or pending['operation'] != operation
                    or pending['ordinal'] != ordinal
                    or event['request'] != pending or event['delivery'] != delivered):
                raise ValueError('native input host and guest focus or physical delivery differ')
            if surface:
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
        self.private_session = None

    def __enter__(self):
        self.lease = open_lease(Path(self.selected['lease']), execution_nonce=self.execution['execution_nonce'],
                               vm_id=self.selected['vm_id'])
        self.actor = self.lease.__enter__()
        try:
            self.log = (self.output / 'native-input.jsonl').open('xb')
            self.errors = (self.output / 'native-input-transport.log').open('xb')
            self.process = subprocess.Popen(self.ssh_command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                            stderr=self.errors, start_new_session=True, bufsize=0)
            # These are exclusively owned SSH transport endpoints, not shared app FDs.
            os.set_blocking(self.process.stdin.fileno(), False)
            os.set_blocking(self.process.stdout.fileno(), False)
            self.selector = selectors.DefaultSelector()
            self.selector.register(self.process.stdout, selectors.EVENT_READ)
            selected = self.exchange({'operation': 'identify-password-fixture-actor',
                'binding': self.execution, 'vm_id': self.selected['vm_id']})
            if not isinstance(selected, dict) or set(selected) != {'state', 'actor'} or selected['state'] != 'password-fixture-actor':
                raise ValueError('private SSH transport omitted its password fixture actor identity')
            self.fixture_actor = password_wire.actor(selected['actor'])
            from .password_exchange import Session
            self.private_session = Session(self.actor, self.fixture_actor, self.execution, self.output)
            self.thread = threading.Thread(target=self._run, name='fixed-native-input', daemon=True)
            self.thread.start()
        except BaseException:
            try:
                self._close_transport()
                if self.private_session is not None:
                    self.private_session.close()
            finally:
                self.lease.__exit__(None, None, None)
            raise
        return self

    def _close_transport(self):
        process = getattr(self, 'process', None)
        if process is not None:
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
            process.stdin.close()
            process.stdout.close()
        selector = getattr(self, 'selector', None)
        if selector is not None:
            selector.close()
        for name in ('log', 'errors'):
            stream = getattr(self, name, None)
            if stream is not None:
                stream.close()

    def exchange(self, document):
        return Pipe(self.process, self.selector, time.monotonic() + 5).line(document)

    def _run(self):
        try:
            while not self.stop.is_set():
                row = self.exchange({'operation': 'poll'})
                if row in ({'state': 'waiting'}, {'state': 'acknowledged'}):
                    self.stop.wait(0.025)
                    continue
                if set(row) == {'state', 'request'} and row['state'] == 'private-request':
                    pipe = Pipe(self.process, self.selector, time.monotonic() + password_wire.MAX_SECONDS)
                    if not self.private_session.perform(row['request'], pipe):
                        # Construct this outside the caught private exception's lifetime.
                        self.error = ValueError('private fixture exchange failed; retained aggregate requires review')
                        self.stop.set()
                        return
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
        self.actor.failed = True
        try:
            self.thread.join(6)
            self._close_transport()
            self.thread.join(password_wire.MAX_SECONDS + 1)
            if self.thread.is_alive():
                self.error = ValueError('fixed native input thread did not settle after its bounded shutdown')
        finally:
            try:
                if self.private_session is not None:
                    self.private_session.close()
            finally:
                self.lease.__exit__(*error)
        if not any(error):
            self.require_healthy()

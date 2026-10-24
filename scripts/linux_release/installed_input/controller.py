"""Bridge one selected guest mailbox to one captured VM's fixed input actor."""
from __future__ import annotations

import os
import hashlib
import re
from pathlib import Path
import secrets
import selectors
import subprocess
import threading
import time

from . import password_wire, protocol, session_wire, surface_wire, unlock_actor, unlock_wire
from ..installed_cases.records import expand_physical_transactions
from .mailbox import decode
from .qemu import open_lease
from .private_pipe import Pipe

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
             ('browser/browser-sandbox-composition', 'browser'),
             ('session/inhibitor-lifetime', 'keepawake'),
             ('session/initial-lock-state', 'lock_initial'),
             ('session/lock-during-presence', 'lock_presence'),
             ('vault/presence-cancel-and-expiry', 'presence')]
    for case, kind in cases:
        leaf = directory / ('leaf/evidence/' + case + '/execution.json')
        if not leaf.exists():
            continue
        if leaf.stat().st_size > 1 << 20:
            raise ValueError('native input guest observations exceed their case bound')
        document = json.loads(leaf.read_bytes())
        observations = document['observations']
        start = len(records)
        if kind in {'presence', 'lock_presence'}:
            from .presence_fixture import FIELDS
            selected = observations.get('launch', {}).get('fixed_input', [])
            if not isinstance(selected, list):
                raise ValueError('presence input requires its fixed transaction list')
            partial = observations.get('native_input')
            has_transitions = bool(observations.get('transitions') or observations.get('lock_transition'))
            expected = ('provider-name-v1', None, 'provider-fixture-credential-v1',
                        *(operation for _, operation, _, _ in FIELDS))
            fixed_partial = partial is not None and (
                kind == 'presence' or (len(selected) < len(expected) and not has_transitions))
            if fixed_partial:
                selected = [*selected, partial]
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
            if kind == 'lock_presence':
                if observations.get('lock_transition') is not None:
                    transitions = [observations['lock_transition']]
                else:
                    transitions = observations.get('transitions', [])
                if not isinstance(transitions, list):
                    raise ValueError('lock-during-presence input requires its fixed transition list')
                if partial is not None and not fixed_partial:
                    transitions = [*transitions, partial]
                expected_transitions = ('session-lock-v1',)
                if len(transitions) > len(expected_transitions) or transitions and len(selected) != len(expected):
                    raise ValueError('lock-during-presence input exceeds or precedes its declared physical sequence')
                records.extend(transitions)
                operations.extend(expected_transitions[:len(transitions)])
        elif kind in {'reply', 'workspace', 'browser', 'keepawake', 'lock_initial'}:
            launch = 'launch_before' if kind == 'reply' else 'launch'
            prompt = 'browser-fixture-prompt-v1' if kind == 'browser' else 'reply-fixture-prompt-v1'
            selected = observations.get(launch, {}).get('fixed_input', [])
            recovery = observations.get('provider_recovery')
            input_limit = 1 if recovery is not None else 4
            if not isinstance(selected, list) or len(selected) > input_limit:
                raise ValueError('reply input exceeds its fixed transaction count')
            partial = observations.get('native_input')
            has_transitions = bool(observations.get('transitions') or observations.get('lock_transition'))
            provider_partial = (kind in {'workspace', 'keepawake', 'lock_initial'} and partial is not None
                and len(selected) < input_limit and not has_transitions)
            if partial is not None and (kind in {'reply', 'browser'} or provider_partial):
                selected = [*selected, partial]
            if len(selected) > input_limit:
                raise ValueError('reply input exceeds its fixed transaction count')
            order = ('provider-name-v1', None, 'provider-fixture-credential-v1', prompt)
            if recovery is not None:
                from ..installed_cases.provider_recovery import registered
                prior = leaf.with_name('provider-registration.bin')
                if prior.stat().st_size > 1 << 20:
                    raise ValueError('provider registration recovery exceeds its retained bound')
                raw = prior.read_bytes()
                binding = {'artifact': document['artifact'], 'source_commit': execution['source_commit'],
                           'native_input': {'binding': execution, 'vm_id': vm_id}}
                account = observations.get(launch, {}).get('session', {}).get('User')
                admitted = registered(json.loads(raw), binding, account, leaf=prior)
                if recovery != {**admitted, 'prior_execution_sha256': hashlib.sha256(raw).hexdigest()} or len(selected) > 1:
                    raise ValueError('provider recovery differs from its retained registration and fixed input order')
                order = (prompt,)
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
            elif kind in {'keepawake', 'lock_initial'}:
                if observations.get('lock_transition') is not None:
                    transitions = [observations['lock_transition']]
                else:
                    transitions = observations.get('transitions', [])
                if not isinstance(transitions, list):
                    raise ValueError('keepawake input requires its fixed transition list')
                if partial is not None and not provider_partial:
                    transitions = [*transitions, partial]
                expected = ('session-lock-v1',)
                if len(transitions) > len(expected) or transitions and len(selected) != input_limit:
                    raise ValueError('keepawake input exceeds or precedes its declared physical sequence')
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
        records[start:] = expand_physical_transactions(leaf, records[start:])
    return records, operations


def verify_deliveries(directory, execution, selected):
    import json
    selected = adapter(selected)
    path = directory / 'native-input.jsonl'
    if selected is None:
        if path.exists():
            raise ValueError('unselected native input controller evidence is forbidden')
        return
    if path.stat().st_size > MAX_LOG:
        raise ValueError('native input delivery log exceeds its bound')
    all_rows = [decode(raw) for raw in path.read_bytes().splitlines()]
    rows, unlock_rows = [], []
    for row_index, row in enumerate(all_rows):
        if (isinstance(row, dict) and isinstance(row.get('request'), dict)
                and row['request'].get('operation') in unlock_wire.OPERATIONS):
            unlock_rows.append((row_index, row))
        else:
            rows.append((row_index, row))
    records, operations = delivery_records(directory, execution, selected['vm_id'])
    if len(records) != len(operations) or any(not isinstance(record, dict) for record in records):
        raise ValueError('native input has unexpected fixed transactions')
    if sum(len(record.get('events', [])) for record in records) != len(rows):
        raise ValueError('native input host and guest delivery counts differ')
    seen, offset = set(), 0
    lock_row_indices = []
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
            row_index, row = rows[offset]
            offset += 1
            if operation == 'session-lock-v1':
                lock_row_indices.append(row_index)
            surface = operation in surface_wire.OPERATIONS
            session = operation in session_wire.OPERATIONS
            after_key = 'surface_after' if surface else 'native_session_after' if session else 'focus_after'
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
            elif session:
                session_wire.same_runtime(pending['native_session'], event['native_session_after'])
                if record['completed'] and ordinal == len(events) - 1 and not session_wire.finished(
                        operation, events[0]['request']['native_session'], event['native_session_after']):
                    raise ValueError('native input has no observed declared session transition')
            elif event['focus_after'] != pending['focus']:
                raise ValueError('native input host and guest focus differ')
    unlock_leaves = [
        directory / 'leaf/evidence/session/inhibitor-lifetime/execution.json',
        directory / 'leaf/evidence/session/initial-lock-state/execution.json',
        directory / 'leaf/evidence/session/lock-during-presence/execution.json',
    ]
    present_unlock_leaves = [leaf for leaf in unlock_leaves if leaf.exists()]
    if unlock_rows or present_unlock_leaves:
        if len(unlock_rows) > 2:
            raise ValueError('native unlock exceeds its single prepare and unlock transactions')
        expected_ops = (unlock_wire.PREPARE, unlock_wire.PURPOSE)[:len(unlock_rows)]
        validated_unlock = []
        for (row_index, row), expected_op in zip(unlock_rows, expected_ops):
            if set(row) != {'request', 'acknowledgment'}:
                raise ValueError('native unlock retained delivery has an unknown shape')
            pending = unlock_wire.request(row['request'], execution)
            ack = unlock_wire.acknowledgment(row['acknowledgment'], pending, selected['vm_id'])
            if pending['operation'] != expected_op:
                raise ValueError('native unlock operations are out of order')
            transaction = protocol.nonce(pending['transaction_id'])
            if transaction in seen:
                raise ValueError('native unlock transaction overlaps or replays')
            seen.add(transaction)
            validated_unlock.append((row_index, pending, ack))
        if len(validated_unlock) == 2:
            (prep_idx, prep_req, _), (unl_idx, unl_req, _) = validated_unlock
            if prep_req['account'] != unl_req['account']:
                raise ValueError('native unlock changed its dedicated account after preparation')
            session_wire.same_runtime(prep_req['native_session'], unl_req['native_session'])
            if not lock_row_indices or not (prep_idx < min(lock_row_indices) <= max(lock_row_indices) < unl_idx):
                raise ValueError('native unlock ordering does not bracket the physical session lock')
        for unlock_leaf in present_unlock_leaves:
            observations = json.loads(unlock_leaf.read_bytes())['observations']
            guest_prep = observations.get('unlock_preparation')
            guest_unlock = observations.get('unlock_transaction')
            if guest_prep is not None:
                if not validated_unlock or guest_prep != {
                        'request': validated_unlock[0][1], 'acknowledgment': validated_unlock[0][2]}:
                    raise ValueError('native credential preparation host and guest records differ')
            if guest_unlock is not None:
                if len(validated_unlock) != 2 or guest_unlock != {
                        'request': validated_unlock[1][1], 'acknowledgment': validated_unlock[1][2]}:
                    raise ValueError('native unlock host and guest records differ')
        proof_path = directory / 'native-unlock.json'
        if proof_path.exists():
            proof = decode(proof_path.read_bytes())
            if (set(proof) != {'format_version', 'binding', 'vm_id', 'account', 'prepare_attempt_id',
                    'unlock_attempt_id', 'secret_sha256', 'pre_locked_at', 'post_unlocked_at', 'cleanup_completed_at'}
                    or proof['format_version'] != 1 or proof['binding'] != protocol.binding(execution)
                    or proof['vm_id'] != selected['vm_id']
                    or not isinstance(proof['secret_sha256'], str)
                    or not re.fullmatch(r'[0-9a-f]{64}', proof['secret_sha256'])
                    or not validated_unlock or proof['prepare_attempt_id'] != validated_unlock[0][1]['transaction_id']
                    or proof['account'] != validated_unlock[0][1]['account']):
                raise ValueError('native unlock proof does not match the retained preparation')
            if len(validated_unlock) == 2:
                if (proof['unlock_attempt_id'] != validated_unlock[1][1]['transaction_id']
                        or not (proof['pre_locked_at'] <= proof['post_unlocked_at'] <= proof['cleanup_completed_at'])):
                    raise ValueError('native unlock proof timestamps or attempt IDs are invalid')


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
        self.unlock_actor = None
        self.prepared_request = None
        self.unlock_proof = None
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

    def _record_log(self, pending, acknowledgment):
        raw = protocol.canonical({'request': pending, 'acknowledgment': acknowledgment})
        self.log.write(raw)
        self.log.flush()
        os.fsync(self.log.fileno())
        self.bytes += len(raw)

    def prepare_unlock(self, pending):
        unlock_wire.request(pending, self.execution)
        if pending['operation'] != unlock_wire.PREPARE:
            raise ValueError('native credential preparation has a foreign operation')
        if self.prepared_request is not None or self.unlock_actor is not None:
            raise ValueError('native credential preparation cannot replay')
        if self.bytes + protocol.MAX_DOCUMENT > MAX_LOG:
            raise ValueError('fixed native input controller log is full before delivery')
        alphabet = b'0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz'
        secret_buf = bytearray(secrets.choice(alphabet) for _ in range(40))
        try:
            secret_sha256 = hashlib.sha256(secret_buf).hexdigest()
            self.unlock_actor = unlock_actor.NativeUnlockActor(
                self.actor, self.execution, pending['account'], bytes(secret_buf))
            ready = self.exchange({'operation': 'native-prepare',
                                   'request_sha256': unlock_wire.digest(pending)})
            if ready != {'state': 'native-prepare-ready', 'request_sha256': unlock_wire.digest(pending),
                         'vm_id': self.selected['vm_id']}:
                raise ValueError('fixed native input guest refused credential preparation readiness')
            pipe = Pipe(self.process, self.selector, time.monotonic() + 20)
            envelope = {'purpose': unlock_wire.PREPARE, 'binding': self.execution,
                        'transaction_id': pending['transaction_id']}
            pipe.send_metadata({**envelope, 'phase': 'prepare'})
            pipe.send_frame(bytes(secret_buf), 40)
            reply = pipe.receive_metadata()
            if reply != {**envelope, 'phase': 'prepared', 'account': pending['account'],
                         'credential_state': 'active'}:
                raise ValueError('fixed native input guest did not settle credential preparation')
        finally:
            secret_buf[:] = b'\0' * len(secret_buf)
        result = {'prepared': True, 'account': dict(pending['account'])}
        acknowledgment = {'format_version': 1, 'request_sha256': unlock_wire.digest(pending),
                          'vm_id': self.selected['vm_id'], 'result': result}
        unlock_wire.acknowledgment(acknowledgment, pending, self.selected['vm_id'])
        self._record_log(pending, acknowledgment)
        if self.exchange({'operation': 'ack', 'acknowledgment': acknowledgment}) != {'state': 'acknowledged'}:
            raise ValueError('fixed native input guest refused its preparation acknowledgment')
        self.prepared_request = pending
        self.unlock_proof = {
            'format_version': 1, 'binding': self.execution, 'vm_id': self.selected['vm_id'],
            'account': dict(pending['account']), 'prepare_attempt_id': pending['transaction_id'],
            'unlock_attempt_id': None, 'secret_sha256': secret_sha256,
            'pre_locked_at': time.time(), 'post_unlocked_at': None, 'cleanup_completed_at': None}
        return acknowledgment

    def unlock_session(self, pending):
        unlock_wire.request(pending, self.execution)
        if pending['operation'] != unlock_wire.PURPOSE:
            raise ValueError('native unlock has a foreign operation')
        if (self.prepared_request is None or self.unlock_actor is None or self.unlock_actor.used
                or self.prepared_request['account'] != pending['account']):
            raise ValueError('native unlock has no unused prepared account custody')
        session_wire.same_runtime(self.prepared_request['native_session'], pending['native_session'])
        if self.bytes + protocol.MAX_DOCUMENT > MAX_LOG:
            raise ValueError('fixed native input controller log is full before delivery')
        ready = self.exchange({'operation': 'native-unlock',
                               'request_sha256': unlock_wire.digest(pending)})
        if ready != {'state': 'native-unlock-ready', 'request_sha256': unlock_wire.digest(pending),
                     'vm_id': self.selected['vm_id']}:
            raise ValueError('fixed native input guest refused native unlock readiness')
        result = self.unlock_actor.perform(self.process, self.selector, time.monotonic() + 25, pending)
        acknowledgment = {'format_version': 1, 'request_sha256': unlock_wire.digest(pending),
                          'vm_id': self.selected['vm_id'], 'result': result}
        unlock_wire.acknowledgment(acknowledgment, pending, self.selected['vm_id'])
        self._record_log(pending, acknowledgment)
        if self.exchange({'operation': 'ack', 'acknowledgment': acknowledgment}) != {'state': 'acknowledged'}:
            raise ValueError('fixed native input guest refused its unlock acknowledgment')
        if self.unlock_proof is not None:
            self.unlock_proof['unlock_attempt_id'] = pending['transaction_id']
            self.unlock_proof['post_unlocked_at'] = time.time()
        return acknowledgment

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
                if isinstance(row['request'], dict) and row['request'].get('operation') in unlock_wire.OPERATIONS:
                    if row['request']['operation'] == unlock_wire.PREPARE:
                        self.prepare_unlock(row['request'])
                    else:
                        self.unlock_session(row['request'])
                    continue
                pending = protocol.request(row['request'], self.execution)
                # Reserve the maximum acknowledgment before any irreversible key delivery.
                if self.bytes + protocol.MAX_DOCUMENT > MAX_LOG:
                    raise ValueError('fixed native input controller log is full before delivery')
                delivered = self.actor.send(pending['operation'], pending['ordinal'],
                                            transaction_id=pending['transaction_id'])
                acknowledgment = {'format_version': 1, 'request_sha256': protocol.request_sha256(pending),
                                  'delivery': delivered}
                protocol.acknowledgment(acknowledgment, pending, self.selected['vm_id'])
                self._record_log(pending, acknowledgment)
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
            if self.unlock_actor is not None:
                self.unlock_actor.close()
            self._close_transport()
            self.thread.join(password_wire.MAX_SECONDS + 1)
            if self.thread.is_alive():
                self.error = ValueError('fixed native input thread did not settle after its bounded shutdown')
            if self.unlock_proof is not None:
                self.unlock_proof['cleanup_completed_at'] = max(
                    time.time(),
                    self.unlock_proof['post_unlocked_at'] or self.unlock_proof['pre_locked_at'])
                (self.output / 'native-unlock.json').write_bytes(protocol.canonical(self.unlock_proof))
        finally:
            try:
                if self.private_session is not None:
                    self.private_session.close()
            finally:
                self.lease.__exit__(*error)
        if not any(error):
            self.require_healthy()

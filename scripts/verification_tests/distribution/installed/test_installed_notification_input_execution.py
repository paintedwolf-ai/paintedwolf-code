"""Notification input needs fresh shell geometry, an absolute pointer and a single owned effect."""
import copy
import hashlib
import io
import json
from pathlib import Path
import tempfile
import threading
import time
import unittest
from types import SimpleNamespace
from unittest.mock import Mock, patch

from linux_release.installed_input import controller, mailbox, notification_shell, notification_wire, protocol, qemu
from verification_tests.distribution.installed.test_notification_wire_execution import proof
from verification_tests.distribution.installed import test_installed_qemu_input_execution as qemu_fixture

EXECUTION = {'format_version': 1, 'execution_nonce': 'a' * 32, 'source_commit': 'b' * 40,
             'driver_tree': 'c' * 40, 'artifact_id': 'app', 'environment_id': 'noble', 'variant_id': 'floor'}
POINTER = {'id': 2, 'return': [{'name': 'owned absolute pointer', 'index': 0, 'current': True, 'absolute': True}]}


def pending():
    return {'format_version': 1, 'binding': EXECUTION, 'transaction_id': 'd' * 32,
            'operation': notification_wire.OPERATION, 'ordinal': 0, 'notification': proof()}


def after():
    value = {key: copy.deepcopy(proof()[key]) for key in ('surface', 'service', 'shell', 'delivery')}
    value['surface']['surfaces'][0].update(visible=True, active=True)
    value.update(activation={'notification_id': 1, 'action': 'default', 'sequence': 4,
                 'token': {'sha256': 'c' * 64, 'sequence': 3}}, selected_session_id=value['delivery']['session_id'])
    return value


class NotificationInputTests(unittest.TestCase):
    def actor(self, pointer=None, *, lost_ack=False):
        actor = qemu_fixture.QemuInputTests().actor()
        actor.connection = Mock()
        def command(name, arguments=None):
            if name == 'query-status':
                return {'id': 1, 'return': {'running': True, 'status': 'running'}}
            if name == 'query-mice':
                return copy.deepcopy(POINTER if pointer is None else pointer)
            if lost_ack:
                raise TimeoutError('actual effect acknowledgment lost')
            return {'id': 3, 'return': {}}
        actor.command.side_effect = command
        return actor

    def test_closed_pointer_request_never_relaxes_focused_keyboard_authority(self):
        request = pending()
        self.assertEqual(protocol.request(request, EXECUTION), request)
        self.assertEqual(protocol.event_sha256(notification_wire.OPERATION, 0, proof()), notification_wire.event_sha256(proof()))
        for change in ({'ordinal': 1}, {'focus': {}}, {'coordinates': [545, 84]}, {'notification': None}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                protocol.request({**request, **change}, EXECUTION)
        with self.assertRaises(ValueError):
            protocol.event_sha256('editor-replace-save-v1', 0, proof())

    def test_unknown_renderer_or_late_capability_reply_never_reaches_qmp(self):
        for change in ('server', 'late', 'missing'):
            value = proof()
            if change == 'server':value['delivery']['server'][2] = '47.0'
            elif change == 'late':value['delivery']['capabilities']['reply_sequence'] = 4
            else:value['delivery'].pop('capabilities')
            actor = self.actor()
            with self.subTest(change=change), self.assertRaises(ValueError):
                actor.send(notification_wire.OPERATION, 0, transaction_id='d' * 32,
                    notification=value, dispatch_deadline=time.monotonic() + 5)
            actor.command.assert_not_called()

    def test_one_actual_vector_follows_current_absolute_pointer_observation(self):
        actor = self.actor()
        delivery = actor.send(notification_wire.OPERATION, 0, transaction_id='d' * 32, notification=proof(), dispatch_deadline=time.monotonic() + 5)
        self.assertEqual([call.args[0] for call in actor.command.call_args_list], ['query-status', 'query-mice', 'input-send-event'])
        self.assertEqual(actor.command.call_args.args, ('input-send-event', {'events': notification_wire.qmp_events(proof())}))
        self.assertEqual(delivery['pointer_query'], POINTER)
        ack = {'format_version': 1, 'request_sha256': protocol.request_sha256(pending()), 'delivery': delivery}
        self.assertEqual(protocol.acknowledgment(ack, pending(), 'owned-test'), delivery)
        with self.assertRaises(ValueError):
            actor.send(notification_wire.OPERATION, 0, transaction_id='d' * 32, notification=proof(), dispatch_deadline=time.monotonic() + 5)
        self.assertEqual(actor.command.call_count, 3)

    def test_relative_ambiguous_or_unknown_pointer_never_dispatches(self):
        relative = copy.deepcopy(POINTER)
        relative['return'][0]['absolute'] = False
        duplicate = copy.deepcopy(POINTER)
        duplicate['return'].append({**duplicate['return'][0], 'index': 1})
        malformed = copy.deepcopy(POINTER)
        malformed['return'][0]['index'] = True
        for pointer in (relative, duplicate, malformed, {'id': 2, 'return': []}):
            with self.subTest(pointer=pointer):
                actor = self.actor(pointer)
                with self.assertRaises(ValueError):
                    actor.send(notification_wire.OPERATION, 0, transaction_id='d' * 32, notification=proof(), dispatch_deadline=time.monotonic() + 5)
                self.assertEqual([call.args[0] for call in actor.command.call_args_list], ['query-status', 'query-mice'])

    def test_uncertain_click_and_changed_delivery_evidence_never_replay_or_qualify(self):
        actor = self.actor(lost_ack=True)
        with self.assertRaises(TimeoutError):
            actor.send(notification_wire.OPERATION, 0, transaction_id='d' * 32, notification=proof(), dispatch_deadline=time.monotonic() + 5)
        with self.assertRaises(ValueError):
            actor.send(notification_wire.OPERATION, 0, transaction_id='e' * 32, notification=proof(), dispatch_deadline=time.monotonic() + 5)
        self.assertEqual(actor.command.call_count, 3)
        actor = self.actor()
        delivery = actor.send(notification_wire.OPERATION, 0, transaction_id='d' * 32, notification=proof(), dispatch_deadline=time.monotonic() + 5)
        for change in ({'pointer_query': {**POINTER, 'id': 4}}, {'event_sha256': 'f' * 64}):
            ack = {'format_version': 1, 'request_sha256': protocol.request_sha256(pending()), 'delivery': {**delivery, **change}}
            with self.assertRaises(ValueError):
                protocol.acknowledgment(ack, pending(), 'owned-test')

    def test_mailbox_requires_fresh_same_target_before_accepting_click_ack(self):
        request = pending()
        actor = self.actor()
        ack = {'format_version': 1, 'request_sha256': protocol.request_sha256(request),
               'delivery': actor.send(notification_wire.OPERATION, 0, transaction_id='d' * 32, notification=proof(), dispatch_deadline=time.monotonic() + 5)}
        for verified in (False, True):
            with self.subTest(verified=verified), tempfile.TemporaryDirectory() as temporary:
                selected = mailbox.Mailbox(EXECUTION, 'owned-test')
                selected.path = Path(temporary) / 'mailbox'
                commands = [{'operation': 'poll'}]
                if verified:
                    commands.append({'operation': 'verify-notification', 'request_sha256': protocol.request_sha256(request)})
                commands.extend([{'operation': 'ack', 'acknowledgment': ack}, {'operation': 'close'}])
                raw = b''.join(protocol.canonical(command) for command in commands)
                with patch.object(mailbox.Mailbox, 'marker'), patch.object(mailbox, 'Mailbox', return_value=selected), \
                        patch.object(mailbox.sys, 'stdin', SimpleNamespace(buffer=io.BytesIO(raw))), \
                        patch.object(mailbox.sys, 'stdout', SimpleNamespace(buffer=io.BytesIO())), \
                        patch.object(notification_shell, 'refresh', return_value=proof()) as refresh:
                    selected.create()
                    selected.write('request.json', request)
                    if verified:
                        mailbox.serve(EXECUTION, 'owned-test')
                        self.assertEqual(selected.read('ack.json'), ack)
                        refresh.assert_called_once_with(request['notification'])
                    else:
                        with self.assertRaisesRegex(ValueError, 'fresh pre-dispatch'):
                            mailbox.serve(EXECUTION, 'owned-test')
                        self.assertIsNone(selected.read('ack.json'))
                        refresh.assert_not_called()

    def test_changed_target_during_predispatch_cannot_reach_acknowledgment(self):
        request = pending()
        changed = proof()
        changed['target']['extent'][0] += 1
        commands = [{'operation': 'poll'}, {'operation': 'verify-notification',
                    'request_sha256': protocol.request_sha256(request)}]
        with tempfile.TemporaryDirectory() as temporary:
            selected = mailbox.Mailbox(EXECUTION, 'owned-test')
            selected.path = Path(temporary) / 'mailbox'
            with patch.object(mailbox.Mailbox, 'marker'), patch.object(mailbox, 'Mailbox', return_value=selected), \
                    patch.object(mailbox.sys, 'stdin', SimpleNamespace(buffer=io.BytesIO(b''.join(protocol.canonical(x) for x in commands)))), \
                    patch.object(mailbox.sys, 'stdout', SimpleNamespace(buffer=io.BytesIO())), \
                    patch.object(notification_shell, 'refresh', return_value=changed):
                selected.create()
                selected.write('request.json', request)
                with self.assertRaises(ValueError):
                    mailbox.serve(EXECUTION, 'owned-test')
                self.assertIsNone(selected.read('ack.json'))

    def test_host_bridge_requires_unchanged_fresh_target_before_single_dispatch(self):
        request = pending()
        delivery = self.actor().send(notification_wire.OPERATION, 0, transaction_id='d' * 32, notification=proof(), dispatch_deadline=time.monotonic() + 5)
        for changed in (False, True):
            with self.subTest(changed=changed), tempfile.TemporaryFile() as log:
                bridge = controller.Bridge.__new__(controller.Bridge)
                bridge.execution, bridge.selected = EXECUTION, {'vm_id': 'owned-test'}
                bridge.log, bridge.bytes, bridge.error = log, 0, None
                bridge.stop = threading.Event()
                bridge.actor = Mock()
                bridge.actor.send.return_value = delivery
                fresh = proof()
                if changed:
                    fresh['target']['extent'][0] += 1
                def exchange(command, **kwargs):
                    if command['operation'] == 'poll':
                        return {'state': 'request', 'request': request}
                    if command['operation'] == 'verify-notification':
                        return {'state': 'verified-notification', 'request_sha256': protocol.request_sha256(request), 'notification': fresh}
                    bridge.stop.set()
                    return {'state': 'acknowledged'}
                bridge.exchange = exchange
                bridge._run()
                if changed:
                    bridge.actor.send.assert_not_called()
                    self.assertIsInstance(bridge.error, ValueError)
                else:
                    self.assertIsNone(bridge.error)
                    bridge.actor.send.assert_called_once()
                    arguments = bridge.actor.send.call_args
                    self.assertEqual(arguments.args, (notification_wire.OPERATION, 0))
                    self.assertEqual(arguments.kwargs['transaction_id'], 'd' * 32)
                    self.assertEqual(arguments.kwargs['notification'], proof())
                    arguments.kwargs['before_effect'](delivery)
                    log.seek(0)
                    retained = json.loads(log.read())
                    self.assertEqual(retained['preflight'], {'request_sha256': protocol.request_sha256(request),
                        'notification_sha256': hashlib.sha256(protocol.canonical(proof())).hexdigest()})

    def test_retained_completed_click_requires_predispatch_proof_and_actual_selected_chat(self):
        request = pending()
        delivery = self.actor().send(notification_wire.OPERATION, 0, transaction_id='d' * 32, notification=proof(), dispatch_deadline=time.monotonic() + 5)
        host = {'request': request, 'acknowledgment': {'format_version': 1,
            'request_sha256': protocol.request_sha256(request), 'delivery': delivery},
            'preflight': {'request_sha256': protocol.request_sha256(request),
                'notification_sha256': hashlib.sha256(protocol.canonical(proof())).hexdigest()}}
        record = {'format_version': 1, 'vm_id': 'owned-test', 'transaction_id': 'd' * 32,
            'operation': notification_wire.OPERATION, 'completed': True, 'text_readback': False,
            'events': [{'request': request, 'delivery': delivery, 'notification_after': after()}]}
        selected = {'format_version': 1, 'kind': 'qemu-fixed-input', 'lease': '/root/owned-lease', 'vm_id': 'owned-test'}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            path = root / 'native-input.jsonl'
            path.write_bytes(protocol.canonical(host))
            with patch.object(controller, 'delivery_records', return_value=([record], [notification_wire.OPERATION])):
                controller.verify_deliveries(root, EXECUTION, selected)
                record['events'][0]['notification_after']['selected_session_id'] = '22222222-2222-4222-8222-222222222222'
                with self.assertRaisesRegex(ValueError, 'selected-chat'):
                    controller.verify_deliveries(root, EXECUTION, selected)
                record['events'][0]['notification_after'] = after()
                del host['preflight']
                path.write_bytes(protocol.canonical(host))
                with self.assertRaisesRegex(ValueError, 'unknown shape'):
                    controller.verify_deliveries(root, EXECUTION, selected)

    def test_qmp_command_only_allows_list_return_for_actual_pointer_inventory(self):
        for name, value, accepted in [('query-mice', POINTER['return'], True),
                ('input-send-event', POINTER['return'], False), ('query-mice', {}, False)]:
            with self.subTest(name=name, value=value):
                actor = qemu.Actor(-1, Path('/unused'), {}, {}, 'a' * 32)
                actor.connection = Mock()
                actor.frame = Mock(return_value={'id': 1, 'return': value})
                if accepted:
                    self.assertEqual(actor.command(name), {'id': 1, 'return': value})
                else:
                    with self.assertRaises(ValueError):
                        actor.command(name)
                self.assertEqual(json.loads(actor.connection.sendall.call_args.args[0]),
                    {'execute': name, 'id': 1})

    def test_notification_records_keep_exact_background_then_single_click_sequence(self):
        initial = [{'operation': op} for op in ('provider-name-v1', 'provider-endpoint-v1-45557',
                    'provider-fixture-credential-v1', 'reply-fixture-prompt-v1')]
        transitions = [{'operation': op} for op in ('workspace-open-peer-v1', 'workspace-hide-main-v1')]
        observations = {'launch': {'fixed_input': initial}, 'transitions': transitions,
                        'notification_click': {'operation': notification_wire.OPERATION}}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            leaf = root / 'leaf/evidence/notifications/activation-token-and-chat/execution.json'
            leaf.parent.mkdir(parents=True)
            def write(value):
                leaf.write_text(json.dumps({'observations': value}))
            with patch.object(controller, 'expand_physical_transactions', side_effect=lambda leaf, rows: rows):
                write(observations)
                records, operations = controller.delivery_records(root, EXECUTION, 'owned-test')
                self.assertEqual(operations, [row['operation'] for row in [*initial, *transitions,
                    observations['notification_click']]])
                self.assertEqual(len(records), 7)
                for changed in ({**observations, 'transitions': transitions[:1]},
                        {**observations, 'transitions': [*transitions, transitions[-1]]},
                        {**observations, 'native_input': observations['notification_click']}):
                    write(changed)
                    with self.assertRaises(ValueError):
                        controller.delivery_records(root, EXECUTION, 'owned-test')
                partial = {**observations, 'native_input': observations['notification_click']}
                del partial['notification_click']
                write(partial)
                self.assertEqual(controller.delivery_records(root, EXECUTION, 'owned-test')[1], operations)

    def test_full_record_retention_is_checked_before_any_pointer_effect(self):
        actor = self.actor()
        actor.command_id = 2
        def refuses(preview):
            self.assertEqual(preview['pointer_query'], POINTER)
            self.assertEqual(preview['qmp_ack'], {'id': 3, 'return': {}})
            protocol.canonical({'request': pending(), 'acknowledgment': preview,
                                'overflow': 'x' * protocol.MAX_DOCUMENT})
        with self.assertRaisesRegex(ValueError, 'exceeds its bound'):
            actor.send(notification_wire.OPERATION, 0, transaction_id='d' * 32,
                       notification=proof(), dispatch_deadline=time.monotonic() + 5, before_effect=refuses)
        self.assertEqual([call.args[0] for call in actor.command.call_args_list],
                         ['query-status', 'query-mice'])
        with self.assertRaises(ValueError):
            actor.send(notification_wire.OPERATION, 0, transaction_id='e' * 32,
                       notification=proof(), dispatch_deadline=time.monotonic() + 5)

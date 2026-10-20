"""A fixed hardware actor cannot widen its VM lease, replay steps or retry effects."""
import copy
import hashlib
import json
import os
from pathlib import Path
import platform
import socket
import tempfile
import threading
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_input import qemu
from linux_release.installed_input.protocol import event_sha256, steps

OPERATION = 'editor-replace-save-v1'


class QemuInputTests(unittest.TestCase):
    def setUp(self):
        delay = patch.object(qemu.time, 'sleep')
        delay.start()
        self.addCleanup(delay.stop)

    def lease(self):
        return {'format_version': 1, 'vm_id': 'owned-test', 'uid': 1000, 'pid': 42,
                'start_time_ticks': 17, 'argv_sha256': 'a' * 64,
                'qmp': {'path': '/tmp/owned/socket', 'device': 1, 'inode': 2},
                'ssh': {'host': '127.0.0.1', 'port': 45557, 'user': 'pwtest'}}

    def test_exact_lease_refuses_unknown_authority_and_numeric_aliases(self):
        with patch.object(qemu.os, 'getuid', return_value=1000):
            self.assertEqual(qemu.selected(self.lease(), 'owned-test'), self.lease())
            for field, value in [('pid', True), ('uid', 0), ('start_time_ticks', '17'),
                                 ('argv_sha256', 'main'), ('vm_id', 'other'), ('command', 'anything')]:
                changed = {**self.lease(), field: value}
                with self.subTest(field=field), self.assertRaises(ValueError):
                    qemu.selected(changed, 'owned-test')
            for replacement in [{'host': 'example.com'}, {'port': True}, {'user': 'user; command'}]:
                changed = self.lease()
                changed['ssh'].update(replacement)
                with self.assertRaises(ValueError):
                    qemu.selected(changed, 'owned-test')
        with self.assertRaises(ValueError):
            qemu.decode('{"pid":42,"pid":43}')

    def actor(self):
        actor = qemu.Actor(-1, Path('/unused'), {'vm_id': 'owned-test'}, self.lease(), 'a' * 32)
        actor.validate = Mock()
        actor.command = Mock(side_effect=lambda name, args=None:
                             {'id': 3, 'return': {'running': True, 'status': 'running'}}
                             if name == 'query-status' else {'id': 4, 'return': {}})
        return actor

    def test_only_next_sealed_step_can_deliver_and_mutation_is_never_retried(self):
        actor = self.actor()
        row = actor.send(OPERATION, 0, transaction_id='b' * 32)
        self.assertEqual(row['event_sha256'], event_sha256(OPERATION, 0))
        self.assertEqual(row['qmp_ack'], {'id': 4, 'return': {}})
        self.assertEqual(actor.command.call_args.args,
                         ('send-key', {'keys': [{'type': 'qcode', 'data': key} for key in steps(OPERATION)[0]], 'hold-time': 80}))
        with self.assertRaises(ValueError):
            actor.send(OPERATION, 0, transaction_id='b' * 32)
        with self.assertRaises(ValueError):
            actor.send(OPERATION, 1, transaction_id='b' * 32)
        self.assertEqual(actor.command.call_count, 2)
        actor = self.actor()
        actor.command.side_effect = [{'id': 3, 'return': {'running': True, 'status': 'running'}},
                                     TimeoutError('fixture lost acknowledgment')]
        with self.assertRaises(TimeoutError):
            actor.send(OPERATION, 0, transaction_id='b' * 32)
        with self.assertRaises(ValueError):
            actor.send(OPERATION, 0, transaction_id='b' * 32)
        self.assertEqual(actor.command.call_count, 2)

    def test_transactions_complete_without_overlap_replay_or_unbounded_growth(self):
        actor = self.actor()
        for ordinal in range(len(steps(OPERATION))):
            actor.send(OPERATION, ordinal, transaction_id='b' * 32)
        actor.send(OPERATION, 0, transaction_id='c' * 32)
        with self.assertRaises(ValueError):
            actor.send(OPERATION, 0, transaction_id='b' * 32)
        actor = self.actor()
        actor.transactions = {f'{number:032x}' for number in range(128)}
        with self.assertRaises(ValueError):
            actor.send(OPERATION, 0, transaction_id='f' * 32)
        actor.command.assert_not_called()
        actor = self.actor()
        actor.send(OPERATION, 0, transaction_id='b' * 32)
        with self.assertRaises(ValueError):
            actor.send(OPERATION, 0, transaction_id='c' * 32)
        self.assertEqual(actor.command.call_count, 2)

    def test_operation_is_pinned_even_if_a_later_catalog_adds_another_operation(self):
        actor = self.actor()
        actor.send(OPERATION, 0, transaction_id='b' * 32)
        with patch.object(qemu, 'steps', return_value=steps(OPERATION)):
            with self.assertRaises(ValueError):
                actor.send('future-declared-operation', 1, transaction_id='b' * 32)
        self.assertEqual(actor.command.call_count, 2)

    def test_unleased_or_writable_files_never_connect(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory).resolve() / 'lease'
            path.write_text(json.dumps(self.lease()))
            with patch.object(qemu.platform, 'system', return_value='Linux'), patch.object(qemu, 'ancestors'), \
                    patch.object(qemu.Actor, 'connect') as connect:
                with self.assertRaises(ValueError):
                    with qemu.open_lease(path, execution_nonce='a' * 32, vm_id='owned-test'):
                        self.fail('writable or user-owned lease accepted')
                connect.assert_not_called()


@unittest.skipUnless(platform.system() == 'Linux', 'actual Linux peer credentials and proc identity required')
class QemuSocketTests(unittest.TestCase):
    def test_actual_peer_connection_binds_process_socket_and_exact_qmp_ack(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            endpoint, lease_path = root / 'qmp.sock', root / 'lease'
            server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            server.bind(str(endpoint)); endpoint.chmod(0o600); server.listen(1); server.settimeout(5)
            self.addCleanup(server.close)
            calls, errors = [], []
            def serve():
                try:
                    connection, _ = server.accept()
                    with connection, connection.makefile('rb') as incoming:
                        connection.settimeout(5)
                        connection.sendall(b'{"QMP":{"version":{},"capabilities":[]}}\r\n')
                        for _ in range(3):
                            row = json.loads(incoming.readline(16385)); calls.append(row)
                            value = {'running': True, 'status': 'running'} if row['execute'] == 'query-status' else {}
                            connection.sendall(json.dumps({'return': value, 'id': row['id']}).encode() + b'\r\n')
                except Exception as error:
                    errors.append(type(error).__name__)
            worker = threading.Thread(target=serve, daemon=True); worker.start()
            stat_fields = Path('/proc/self/stat').read_bytes().rpartition(b') ')[2].split()
            info = endpoint.lstat()
            value = {'format_version': 1, 'vm_id': 'owned-test', 'uid': os.getuid(), 'pid': os.getpid(),
                     'start_time_ticks': int(stat_fields[19]),
                     'argv_sha256': hashlib.sha256(Path('/proc/self/cmdline').read_bytes()).hexdigest(),
                     'qmp': {'path': str(endpoint), 'device': info.st_dev, 'inode': info.st_ino},
                     'ssh': {'host': '127.0.0.1', 'port': 45557, 'user': 'pwtest'}}
            raw = json.dumps(value).encode(); lease_path.write_bytes(raw)
            descriptor = os.open(lease_path, os.O_RDONLY)
            identity = {'vm_id': 'owned-test', 'sha256': hashlib.sha256(raw).hexdigest(),
                        'file': list(qemu.stable(os.fstat(descriptor))), 'vm': value}
            actor = qemu.Actor(descriptor, lease_path, identity, value, 'a' * 32)
            try:
                actor.connect()
                result = actor.send(OPERATION, 0, transaction_id='b' * 32)
                self.assertEqual(result['qmp_ack'], {'id': 3, 'return': {}})
                actor.value = copy.deepcopy(value)
                actor.value['argv_sha256'] = '0' * 64
                with self.assertRaises((OSError, ValueError)):
                    actor.send(OPERATION, 1, transaction_id='b' * 32)
            finally:
                actor.close(); os.close(descriptor); worker.join(6)
            self.assertFalse(worker.is_alive())
            self.assertEqual(errors, [])
            self.assertEqual([row['execute'] for row in calls], ['qmp_capabilities', 'query-status', 'send-key'])
            self.assertEqual(calls[-1]['arguments'], {'keys': [{'type': 'qcode', 'data': 'ctrl'},
                                                               {'type': 'qcode', 'data': 'a'}], 'hold-time': 80})

"""Private physical password fixtures retain aggregate facts, never key values/hashes."""
import hashlib
import json
from pathlib import Path
import time
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_input import password_actor, password_wire, qemu
from linux_release.installed_input.protocol import steps


class PrivateActorTests(unittest.TestCase):
    def setUp(self):
        self.delay = patch.object(qemu.time, 'sleep')
        self.delay.start()
        self.addCleanup(self.delay.stop)
        self.secret = 'aB3' * 13 + '7'
        identity = {'pid': 123, 'parent_pid': 1, 'start_time_ticks': 42}
        focus = {'launch': identity, 'entry': identity, 'frame': identity,
            'frame_lineage': [identity], 'entry_lineage': [identity],
            'accessible_chain': [{'bus_name': ':1.2', 'path': '/frame', 'pid': 123}],
            'entry_focused': True, 'frame_active': True, 'frame_visible': True}
        execution = {'format_version': 1, 'execution_nonce': 'a' * 32,
            'source_commit': 'b' * 40, 'driver_tree': 'c' * 40,
            'artifact_id': 'app', 'environment_id': 'fedora', 'variant_id': 'gnome-wayland'}
        self.pending = {'format_version': 1, 'binding': execution, 'operation': password_wire.OPERATION,
            'stage': 'new-app-password', 'transaction_id': 'd' * 32,
            'actor': {'process': identity, 'uid': 1234, 'session_nonce': 'e' * 32}, 'case': identity,
            'profile': {'path': '/home/fixture/.config/paintedwolf', 'device': 1, 'inode': 2, 'uid': 1234},
            'session': {'id': '123', 'User': '1234', 'Type': 'wayland', 'Active': 'yes', 'Remote': 'no'},
            'target': {'kind': 'entry', 'label': 'New app password', 'focus': focus}, 'expires_ns': 12345}
        self.actor = qemu.Actor(-1, Path('/unused'), {'vm_id': 'owned-test'}, {'vm_id': 'owned-test'}, 'a' * 32)
        self.actor.validate = Mock()
        self.actor.command = Mock(side_effect=lambda name, args=None:
            {'id': 3, 'return': {'running': True, 'status': 'running'}} if name == 'query-status'
            else {'id': 4, 'return': {}})

    def test_only_fixed_fixture_sequence_is_sent_and_outcome_contains_no_character_or_hash(self):
        fixture = password_actor.Fixture(self.actor, self.pending, self.secret)
        with patch.object(qemu, 'event_sha256', side_effect=AssertionError('private key hash forbidden')):
            with fixture:
                for ordinal in range(41):
                    fixture.deliver(ordinal)
                outcome = fixture.finish()
        keys = [call.args[1]['keys'] for call in self.actor.command.call_args_list if call.args[0] == 'send-key']
        expected = [[{'type': 'qcode', 'data': 'ctrl'}, {'type': 'qcode', 'data': 'a'}]]
        expected += [[*([{'type': 'qcode', 'data': 'shift'}] if char.isupper() else []),
                      {'type': 'qcode', 'data': char.lower()}] for char in self.secret]
        self.assertEqual(keys, expected)
        self.assertTrue(outcome['completed'])
        self.assertFalse(outcome['delivery_uncertain'])
        self.assertEqual(outcome['acknowledged_count'], 41)
        raw = json.dumps(outcome)
        for forbidden in (self.secret, 'event_sha256', 'qmp_ack', 'ordinal',
                          hashlib.sha256(self.secret.encode()).hexdigest(),
                          *(hashlib.sha256(json.dumps([char], separators=(',', ':')).encode()).hexdigest()
                            for char in set(self.secret.lower()))):
            self.assertNotIn(forbidden, raw)
        self.assertIsNone(fixture._value)
        self.assertIsNone(self.actor.deadline)
        self.assertFalse(self.actor.failed)

    def test_incomplete_or_replayed_input_fails_closed_and_discards_private_reference(self):
        fixture = password_actor.Fixture(self.actor, self.pending, self.secret)
        with self.assertRaises(ValueError):
            with fixture:
                fixture.deliver(0)
                fixture.deliver(0)
        self.assertTrue(self.actor.failed)
        self.assertIsNone(fixture._value)
        self.assertFalse(fixture.outcome()['completed'])
        self.assertEqual(fixture.outcome()['acknowledged_count'], 1)
        self.assertTrue(self.actor.lock.acquire(blocking=False))
        self.actor.lock.release()

    def test_catching_invalid_ordinal_cannot_resume_or_finish_the_transaction(self):
        fixture = password_actor.Fixture(self.actor, self.pending, self.secret)
        with self.assertRaises(ValueError):
            with fixture:
                fixture.deliver(0)
                with self.assertRaises(ValueError):
                    fixture.deliver(0)
                self.assertTrue(self.actor.failed)
                self.assertIsNone(fixture._value)
                with self.assertRaises(ValueError):
                    fixture.deliver(1)
                with self.assertRaises(ValueError):
                    fixture.finish()
        self.assertEqual(self.actor.command.call_count, 2)
        self.assertFalse(fixture.outcome()['completed'])

    def test_failed_qmp_ack_retains_only_partial_count_and_never_retries_mutation(self):
        self.actor.command.side_effect = [{'id': 3, 'return': {'running': True, 'status': 'running'}},
                                          TimeoutError('fixture acknowledgment unavailable')]
        fixture = password_actor.Fixture(self.actor, self.pending, self.secret)
        with self.assertRaises(TimeoutError):
            with fixture:
                fixture.deliver(0)
        self.assertTrue(fixture.outcome()['delivery_uncertain'])
        self.assertEqual(fixture.outcome()['acknowledged_count'], 0)
        self.assertIsNone(fixture._value)
        with self.assertRaises(ValueError):
            self.actor.send('editor-find-needle-v1', 0, transaction_id='f' * 32)
        self.assertEqual(self.actor.command.call_count, 2)

    def test_deadline_refuses_input_before_mutation_and_an_unfinished_context_cannot_succeed(self):
        fixture = password_actor.Fixture(self.actor, self.pending, self.secret)
        with self.assertRaises(ValueError):
            with fixture:
                self.actor.deadline = time.monotonic() - 1
                fixture.deliver(0)
        self.actor.command.assert_not_called()
        self.assertIsNone(fixture._value)
        self.assertTrue(self.actor.failed)

    def test_private_and_public_transactions_share_overlap_and_replay_authority(self):
        operation = 'editor-find-needle-v1'
        for ordinal in range(len(steps(operation))):
            self.actor.send(operation, ordinal, transaction_id='f' * 32)
        with password_actor.Fixture(self.actor, self.pending, self.secret) as fixture:
            for ordinal in range(41):
                fixture.deliver(ordinal)
            fixture.finish()
        self.actor.send(operation, 0, transaction_id='1' * 32)
        with self.assertRaises(ValueError):
            with password_actor.Fixture(self.actor, self.pending, self.secret):
                pass
        self.assertTrue(self.actor.failed)


class PrivateQmpDeadlineTests(unittest.TestCase):
    def test_partial_qmp_frames_do_not_extend_the_private_transaction_deadline(self):
        import socket
        import threading
        actor = qemu.Actor(-1, Path('/unused'), {'vm_id': 'owned-test'}, {'vm_id': 'owned-test'}, 'a' * 32)
        first, second = socket.socketpair()
        stop = threading.Event()
        def partial_frames():
            while not stop.wait(0.01):
                second.sendall(b' ')
        worker = threading.Thread(target=partial_frames)
        try:
            actor.connection = first
            actor.deadline = time.monotonic() + 0.05
            started = time.monotonic()
            worker.start()
            with self.assertRaises((ValueError, TimeoutError)):
                actor.command('query-status')
            self.assertLess(time.monotonic() - started, 1)
        finally:
            stop.set()
            worker.join(2)
            first.close()
            second.close()
        self.assertFalse(worker.is_alive())

    def test_cancellation_during_query_status_prevents_the_following_key_mutation(self):
        def query(name, arguments=None):
            self.assertEqual(name, 'query-status')
            self.actor.failed = True
            return {'id': 3, 'return': {'running': True, 'status': 'running'}}
        self.actor.command.side_effect = query
        with self.assertRaises(ValueError):
            with password_actor.Fixture(self.actor, self.pending, self.secret) as fixture:
                fixture.deliver(0)
        self.assertEqual(self.actor.command.call_count, 1)
        self.assertTrue(self.actor.failed)
        self.assertIsNone(fixture._value)

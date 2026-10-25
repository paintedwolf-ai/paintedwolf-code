"""Fresh notification proof expires across queries, retention, and asynchronous QMP frames."""
import json
import threading
from pathlib import Path
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_input import controller, notification_wire, qemu
from verification_tests.distribution.installed import test_installed_notification_input_execution as fixture
from verification_tests.distribution.installed.test_notification_wire_execution import proof


class NotificationDispatchDeadlineTests(unittest.TestCase):
    def test_fresh_exchange_consumes_host_deadline_before_actor_is_called(self):
        bridge = controller.Bridge.__new__(controller.Bridge)
        bridge.execution, bridge.bytes, bridge.error = fixture.EXECUTION, 0, None
        bridge.stop, bridge.actor = threading.Event(), Mock()
        clock, seen = [0], []
        request = fixture.pending()
        def exchange(command, **kwargs):
            if command['operation'] == 'poll':
                return {'state': 'request', 'request': request}
            seen.append(kwargs['deadline'])
            clock[0] = 5
            return {'state': 'verified-notification', 'request_sha256': controller.protocol.request_sha256(request),
                'notification': proof()}
        bridge.exchange = exchange
        with patch.object(controller.time, 'monotonic', side_effect=lambda: clock[0]):
            bridge._run()
        self.assertEqual(seen, [5])
        self.assertIsInstance(bridge.error, ValueError)
        bridge.actor.send.assert_not_called()

    def test_delayed_pointer_query_or_retention_never_clicks_or_replays(self):
        for stage in ('query-mice', 'retention'):
            with self.subTest(stage=stage):
                actor = fixture.NotificationInputTests().actor()
                clock = [0]
                original = actor.command.side_effect
                def command(name, arguments=None):
                    result = original(name, arguments)
                    if name == stage:
                        clock[0] = 5
                    return result
                actor.command.side_effect = command
                def retention(_):
                    if stage == 'retention':
                        clock[0] = 5
                with patch.object(qemu.time, 'monotonic', side_effect=lambda: clock[0]):
                    with self.assertRaisesRegex(ValueError, 'absolute proof deadline'):
                        actor.send(notification_wire.OPERATION, 0, transaction_id='d' * 32,
                            notification=proof(), before_effect=retention, dispatch_deadline=5)
                    with self.assertRaises(ValueError):
                        actor.send(notification_wire.OPERATION, 0, transaction_id='e' * 32,
                            notification=proof(), dispatch_deadline=10)
                self.assertEqual([row.args[0] for row in actor.command.call_args_list],
                    ['query-status', 'query-mice'])

    def test_async_frames_and_partial_frame_trickles_consume_same_absolute_deadline(self):
        for partial in (False, True):
            with self.subTest(partial=partial):
                actor = qemu.Actor(-1, Path('/unused'), {}, {}, 'a' * 32)
                actor.dispatch_deadline = 5
                actor.connection = Mock()
                clock = [0]
                def receive(_):
                    clock[0] += 2
                    return b' ' if partial else b'{"event":"DISPLAY"}\n'
                actor.connection.recv.side_effect = receive
                with patch.object(qemu.time, 'monotonic', side_effect=lambda: clock[0]):
                    with self.assertRaisesRegex(ValueError, 'absolute proof deadline'):
                        actor.command('query-mice')
                self.assertLessEqual(actor.connection.recv.call_count, 3)
                self.assertEqual(json.loads(actor.connection.sendall.call_args.args[0])['execute'], 'query-mice')
                timeouts = [row.args[0] for row in actor.connection.settimeout.call_args_list]
                self.assertTrue(all(0 < value <= 3 for value in timeouts))
                self.assertIn(1, timeouts)

"""The native input controller supervises one guest process and bridges one mailbox to one captured VM."""
import json
import os
from pathlib import Path
import selectors
import subprocess
import tempfile
import unittest
from unittest.mock import MagicMock, Mock, patch

from linux_release.installed_input import controller, guest, protocol

EXECUTION = {'format_version': 1, 'execution_nonce': 'a' * 32, 'source_commit': 'b' * 40, 'driver_tree': 'c' * 40,
             'artifact_id': 'app', 'environment_id': 'fedora', 'variant_id': 'gnome-wayland'}
SELECTED = {'format_version': 1, 'kind': 'qemu-fixed-input', 'lease': '/root/lease', 'vm_id': 'owned'}
IDENTITY = {'pid': 123, 'parent_pid': 1, 'start_time_ticks': 42}
FOCUS = {'launch': IDENTITY, 'entry': IDENTITY, 'frame': IDENTITY,
         'frame_lineage': [IDENTITY], 'entry_lineage': [IDENTITY],
         'accessible_chain': [{'bus_name': ':1.2', 'path': '/frame', 'pid': IDENTITY['pid']}],
         'entry_focused': True, 'frame_active': True, 'frame_visible': True}
OPERATION = 'editor-replace-save-v1'


def pending_request(ordinal=0, transaction='d' * 32):
    return {'format_version': 1, 'binding': EXECUTION, 'transaction_id': transaction, 'operation': OPERATION,
            'ordinal': ordinal, 'focus': FOCUS}


def delivery(pending):
    return {'format_version': 1, 'execution_nonce': 'a' * 32, 'vm_id': 'owned', 'transaction_id': pending['transaction_id'],
            'operation': pending['operation'], 'ordinal': pending['ordinal'],
            'event_sha256': protocol.event_sha256(pending['operation'], pending['ordinal']),
            'lease': {'vm_id': 'owned'}, 'qmp_ack': {'id': pending['ordinal'] + 1, 'return': {}}}


class ManagedProcessTests(unittest.TestCase):
    def supervise(self, bridge, poll, wait=None):
        process = MagicMock()
        process.poll.side_effect = poll
        process.returncode = 0
        if wait is not None:
            process.wait.side_effect = wait
        with tempfile.TemporaryDirectory() as temporary, patch.object(controller.subprocess, 'Popen', return_value=process) as popen, \
                patch.object(controller.time, 'sleep') as sleep:
            output = Path(temporary) / 'guest.log'
            code = controller.run_managed(bridge, ['guest', '--fixed'], output)
            self.assertTrue(output.exists())
        self.assertEqual(popen.call_args.args[0], ['guest', '--fixed'])
        self.assertEqual(popen.call_args.kwargs['stderr'], subprocess.STDOUT)
        return code, process, sleep

    def test_the_guest_runs_to_completion_while_the_bridge_stays_healthy(self):
        bridge = Mock()
        code, process, sleep = self.supervise(bridge, [None, None, 0])
        self.assertEqual(code, 0)
        self.assertEqual(bridge.require_healthy.call_count, 2)
        self.assertEqual(sleep.call_count, 2)
        process.terminate.assert_not_called()
        code, process, _ = self.supervise(None, [None, 0])
        self.assertEqual(code, 0)

    def test_a_failed_bridge_terminates_then_kills_the_guest_and_propagates(self):
        bridge = Mock()
        bridge.require_healthy.side_effect = ValueError('bridge failed')
        with self.assertRaisesRegex(ValueError, 'bridge failed'):
            self.supervise(bridge, [None])
        with self.assertRaisesRegex(ValueError, 'bridge failed'):
            code, process, _ = self.supervise(bridge, [None], wait=[subprocess.TimeoutExpired('guest', 5), None])
        process = MagicMock()
        process.poll.side_effect = [None]
        process.wait.side_effect = [subprocess.TimeoutExpired('guest', 5), None]
        with tempfile.TemporaryDirectory() as temporary, patch.object(controller.subprocess, 'Popen', return_value=process):
            with self.assertRaises(ValueError):
                controller.run_managed(bridge, ['guest'], Path(temporary) / 'guest.log')
        process.terminate.assert_called_once_with()
        process.kill.assert_called_once_with()
        self.assertEqual(process.wait.call_count, 2)

    def test_an_existing_log_is_never_replaced(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(controller.subprocess, 'Popen') as popen:
            output = Path(temporary) / 'guest.log'
            output.write_text('retained')
            with self.assertRaises(FileExistsError):
                controller.run_managed(None, ['guest'], output)
            popen.assert_not_called()
            self.assertEqual(output.read_text(), 'retained')


class DeliveryVerificationTests(unittest.TestCase):
    def mailbox(self):
        box = Mock()

        def read(name):
            pending = box.write.call_args.args[1]
            return {'format_version': 1, 'request_sha256': protocol.request_sha256(pending), 'delivery': delivery(pending)}
        box.read.side_effect = read
        return box

    def perform(self, operation=OPERATION):
        actor = Mock()
        actor.observe.return_value = FOCUS
        return guest.perform(EXECUTION, 'owned', actor, self.mailbox(), operation=operation)

    @staticmethod
    def rows(records):
        return [protocol.canonical({'request': event['request'], 'acknowledgment': {
            'format_version': 1, 'request_sha256': protocol.request_sha256(event['request']), 'delivery': event['delivery']}})
                for record in records for event in record['events']]

    @staticmethod
    def write(root, observations, rows):
        leaf = root / 'leaf/evidence/files/edit-search-review'
        leaf.mkdir(parents=True, exist_ok=True)
        (leaf / 'execution.json').write_text(json.dumps({'observations': observations}))
        (root / 'native-input.jsonl').write_bytes(b''.join(rows))

    def test_unselected_input_forbids_any_delivery_log(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            controller.verify_deliveries(root, EXECUTION, None)
            (root / 'native-input.jsonl').write_bytes(b'')
            with self.assertRaisesRegex(ValueError, 'forbidden'):
                controller.verify_deliveries(root, EXECUTION, None)

    def test_oversized_logs_or_observations_are_refused_before_parsing(self):
        edit = self.perform()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.write(root, {'editor_keyboard': {'physical_vm_keyboard': edit}}, self.rows([edit]))
            with patch.object(controller, 'MAX_LOG', 16), self.assertRaisesRegex(ValueError, 'log exceeds'):
                controller.verify_deliveries(root, EXECUTION, SELECTED)
            (root / 'leaf/evidence/files/edit-search-review/execution.json').write_bytes(b' ' * ((1 << 20) + 1))
            with self.assertRaisesRegex(ValueError, 'case bound'):
                controller.verify_deliveries(root, EXECUTION, SELECTED)

    def test_more_transactions_than_the_fixed_operations_are_refused(self):
        edit, search = self.perform(), self.perform('editor-find-needle-v1')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            observations = {'editor_keyboard': {'physical_vm_keyboard': edit},
                            'editor_search_keyboard': {'physical_vm_keyboard': search}, 'native_input': search}
            self.write(root, observations, self.rows([edit, search, search]))
            with self.assertRaisesRegex(ValueError, 'unexpected fixed transactions'):
                controller.verify_deliveries(root, EXECUTION, SELECTED)

    def test_host_rows_must_match_the_guest_events_shape_and_focus(self):
        edit = self.perform()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            rows = self.rows([edit])
            renamed = json.loads(rows[0])
            renamed['receipt'] = renamed.pop('acknowledgment')
            self.write(root, {'editor_keyboard': {'physical_vm_keyboard': edit}}, [protocol.canonical(renamed), *rows[1:]])
            with self.assertRaisesRegex(ValueError, 'unknown shape'):
                controller.verify_deliveries(root, EXECUTION, SELECTED)
            moved = json.loads(json.dumps(edit))
            moved['events'][0]['focus_after'] = {**FOCUS, 'frame_active': False}
            self.write(root, {'editor_keyboard': {'physical_vm_keyboard': moved}}, rows)
            with self.assertRaisesRegex(ValueError, 'focus or physical delivery differ'):
                controller.verify_deliveries(root, EXECUTION, SELECTED)


class Guest:
    """A pipe standing in for the guest's mailbox transport, prefilled with its responses."""

    def __init__(self, responses):
        self.read_end, self.write_end = os.pipe()
        self.stdout = os.fdopen(self.read_end, 'rb', buffering=0)
        os.write(self.write_end, b''.join(protocol.canonical(row) for row in responses))
        os.close(self.write_end)
        self.process = MagicMock()
        self.process.stdout = self.stdout
        self.process.poll.return_value = 0


class BridgeTests(unittest.TestCase):
    def bridge(self, responses, send=None, max_log=controller.MAX_LOG):
        guest_pipe = Guest(responses)
        actor = Mock()
        lease = MagicMock()
        lease.__enter__.return_value = actor
        self.temporary = tempfile.TemporaryDirectory()
        output = Path(self.temporary.name)
        bridge = controller.Bridge(SELECTED, EXECUTION, ['ssh', 'guest'], output)
        actor.send.side_effect = None if send is None else (lambda *a, **k: send(bridge, *a, **k))
        with patch.object(controller, 'open_lease', return_value=lease) as opened, \
                patch.object(controller.subprocess, 'Popen', return_value=guest_pipe.process) as popen, \
                patch.object(controller, 'MAX_LOG', max_log):
            bridge.__enter__()
            bridge.thread.join(5)
        self.assertFalse(bridge.thread.is_alive())
        opened.assert_called_once_with(Path('/root/lease'), execution_nonce='a' * 32, vm_id='owned')
        self.assertEqual(popen.call_args.args[0], ['ssh', 'guest'])
        self.assertTrue(popen.call_args.kwargs['start_new_session'])
        return bridge, actor, lease, guest_pipe, output

    def tearDown(self):
        if hasattr(self, 'temporary'):
            self.temporary.cleanup()

    def test_each_guest_request_is_delivered_once_logged_and_acknowledged(self):
        pending = pending_request()

        def send(bridge, operation, ordinal, *, transaction_id):
            bridge.stop.set()
            return delivery(pending)
        bridge, actor, lease, guest_pipe, output = self.bridge(
            [{'state': 'waiting'}, {'state': 'acknowledged'}, {'state': 'request', 'request': pending}, {'state': 'acknowledged'}], send)
        bridge.__exit__(None, None, None)
        actor.send.assert_called_once_with(OPERATION, 0, transaction_id='d' * 32)
        acknowledgment = {'format_version': 1, 'request_sha256': protocol.request_sha256(pending), 'delivery': delivery(pending)}
        expected = protocol.canonical({'request': pending, 'acknowledgment': acknowledgment})
        self.assertEqual((output / 'native-input.jsonl').read_bytes(), expected)
        self.assertEqual(bridge.bytes, len(expected))
        self.assertTrue((output / 'native-input-transport.log').exists())
        written = [call.args[0] for call in guest_pipe.process.stdin.write.call_args_list]
        self.assertEqual(written, [protocol.canonical({'operation': 'poll'})] * 3
                         + [protocol.canonical({'operation': 'ack', 'acknowledgment': acknowledgment})])
        lease.__exit__.assert_called_once_with(None, None, None)
        self.assertIsNone(bridge.error)
        self.assertTrue(guest_pipe.stdout.closed)

    def test_an_unknown_response_or_refused_acknowledgment_fails_the_bridge(self):
        pending = pending_request()
        for responses in [[{'state': 'other'}], [{'state': 'request', 'request': pending}, {'state': 'waiting'}]]:
            with self.subTest(responses=responses):
                bridge, actor, lease, _, _ = self.bridge(responses, lambda bridge, *a, **k: delivery(pending))
                with self.assertRaisesRegex(ValueError, 'requires review'):
                    bridge.__exit__(None, None, None)
                self.assertIsInstance(bridge.error, ValueError)
                lease.__exit__.assert_called_once_with(None, None, None)
                self.tearDown()

    def test_a_full_controller_log_refuses_before_any_key_delivery(self):
        pending = pending_request()
        bridge, actor, _, _, _ = self.bridge([{'state': 'request', 'request': pending}], lambda bridge, *a, **k: delivery(pending), max_log=100)
        with self.assertRaises(ValueError):
            bridge.__exit__(None, None, None)
        actor.send.assert_not_called()
        self.assertRegex(str(bridge.error), 'log is full')

    def test_a_bridge_exiting_on_an_error_keeps_that_error(self):
        bridge, _, lease, _, _ = self.bridge([{'state': 'other'}], None)
        bridge.__exit__(ValueError, ValueError('guest failed'), None)
        lease.__exit__.assert_called_once()
        self.assertIsInstance(bridge.error, ValueError)

    def test_a_transport_that_cannot_start_releases_the_lease(self):
        lease = MagicMock()
        with tempfile.TemporaryDirectory() as temporary, patch.object(controller, 'open_lease', return_value=lease), \
                patch.object(controller.subprocess, 'Popen', side_effect=OSError('no ssh')):
            bridge = controller.Bridge(SELECTED, EXECUTION, ['ssh'], Path(temporary))
            with self.assertRaisesRegex(OSError, 'no ssh'):
                bridge.__enter__()
        lease.__exit__.assert_called_once_with(None, None, None)

    def test_a_foreign_selection_or_execution_cannot_build_a_bridge(self):
        with self.assertRaises(ValueError):
            controller.Bridge({**SELECTED, 'kind': 'ordinary'}, EXECUTION, ['ssh'], Path('/unused'))
        with self.assertRaises(ValueError):
            controller.Bridge(SELECTED, {**EXECUTION, 'execution_nonce': 'short'}, ['ssh'], Path('/unused'))
        self.assertIsNone(controller.adapter(None))


class ExchangeTests(unittest.TestCase):
    def exchange(self, raw):
        bridge = controller.Bridge.__new__(controller.Bridge)
        read_end, write_end = os.pipe()
        os.write(write_end, raw)
        os.close(write_end)
        bridge.process = Mock()
        bridge.process.stdout = os.fdopen(read_end, 'rb', buffering=0)
        bridge.selector = selectors.DefaultSelector()
        bridge.selector.register(bridge.process.stdout, selectors.EVENT_READ)
        try:
            return bridge.exchange({'operation': 'poll'})
        finally:
            bridge.selector.close()
            bridge.process.stdout.close()

    def test_a_closed_transport_or_an_unterminated_response_is_refused(self):
        self.assertEqual(self.exchange(b'{"state":"waiting"}\n'), {'state': 'waiting'})
        with self.assertRaisesRegex(ValueError, 'ended before'):
            self.exchange(b'')
        with self.assertRaisesRegex(ValueError, 'response bound'):
            self.exchange(b'x' * (protocol.MAX_DOCUMENT + 2))


if __name__ == '__main__':
    unittest.main()

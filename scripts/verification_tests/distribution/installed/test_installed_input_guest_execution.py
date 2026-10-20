"""A guest requires exact physical delivery and stable focus for every fixed step."""
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_input import controller, guest, protocol


class GuestInputTests(unittest.TestCase):
    def test_mailbox_transport_has_one_deadline_even_when_bytes_keep_arriving(self):
        bridge = controller.Bridge.__new__(controller.Bridge)
        bridge.process, bridge.selector = Mock(), Mock()
        bridge.selector.select.return_value = True
        with patch.object(controller.time, 'monotonic', side_effect=[0, 1, 6]), \
                patch.object(controller.os, 'read', return_value=b' '):
            with self.assertRaisesRegex(ValueError, 'response time'):
                bridge.exchange({'operation': 'poll'})
        bridge.selector.select.assert_called_once_with(4)

    def setUp(self):
        self.execution = {'format_version': 1, 'execution_nonce': 'a' * 32,
            'source_commit': 'b' * 40, 'driver_tree': 'c' * 40,
            'artifact_id': 'app', 'environment_id': 'fedora', 'variant_id': 'gnome-wayland'}
        identity = {'pid': 123, 'parent_pid': 1, 'start_time_ticks': 42}
        self.focus = {'launch': identity, 'entry': identity, 'frame': identity,
            'entry_focused': True, 'frame_active': True, 'frame_visible': True}

    def mailbox(self, stale=False):
        box = Mock()
        def read(name):
            pending = box.write.call_args.args[1]
            delivery = {'format_version': 1, 'execution_nonce': 'd' * 32 if stale else 'a' * 32,
                'vm_id': 'owned', 'transaction_id': pending['transaction_id'],
                'operation': pending['operation'], 'ordinal': pending['ordinal'],
                'event_sha256': protocol.event_sha256(pending['operation'], pending['ordinal']),
                'lease': {'vm_id': 'owned'}, 'qmp_ack': {'id': pending['ordinal'] + 1, 'return': {}}}
            return {'format_version': 1, 'request_sha256': protocol.request_sha256(pending), 'delivery': delivery}
        box.read.side_effect = read
        return box

    def test_full_fixed_sequence_requires_every_ack_and_focus_before_after(self):
        actor, box = Mock(), self.mailbox()
        actor.observe.return_value = self.focus
        result = guest.perform(self.execution, 'owned', actor, box)
        count = len(protocol.steps('editor-replace-save-v1'))
        self.assertTrue(result['completed'])
        self.assertEqual(len(result['events']), count)
        self.assertEqual(actor.observe.call_count, count * 2)
        self.assertEqual(box.clear_completed.call_count, count)
        self.assertEqual([event['request']['ordinal'] for event in result['events']], list(range(count)))
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            leaf = root / 'leaf/evidence/files/edit-search-review'
            leaf.mkdir(parents=True)
            import json
            (leaf / 'execution.json').write_text(json.dumps({'observations': {'editor_keyboard': {'physical_vm_keyboard': result}}}))
            rows = []
            for event in result['events']:
                rows.append(protocol.canonical({'request': event['request'], 'acknowledgment': {
                    'format_version': 1, 'request_sha256': protocol.request_sha256(event['request']), 'delivery': event['delivery']}}))
            (root / 'native-input.jsonl').write_bytes(b''.join(rows))
            selected = {'format_version': 1, 'kind': 'qemu-fixed-input', 'lease': '/root/lease', 'vm_id': 'owned'}
            controller.verify_deliveries(root, self.execution, selected)
            (root / 'native-input.jsonl').write_bytes(b''.join(rows[:-1]))
            with self.assertRaisesRegex(ValueError, 'counts differ'):
                controller.verify_deliveries(root, self.execution, selected)

    def test_stale_ack_and_focus_change_never_clear_or_retry_mutating_request(self):
        for stale in (True, False):
            box, actor = self.mailbox(stale), Mock()
            actor.observe.side_effect = [self.focus, {**self.focus, 'frame_active': False}]
            with self.subTest(stale=stale), self.assertRaises(ValueError) as refusal:
                guest.perform(self.execution, 'owned', actor, box)
            self.assertEqual(box.write.call_count, 1)
            box.clear_completed.assert_not_called()
            self.assertFalse(refusal.exception.observations['native_input']['completed'])


if __name__ == '__main__':
    unittest.main()

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
        patcher = patch.object(guest.time, 'sleep')
        self.sleep = patcher.start()
        self.addCleanup(patcher.stop)
        self.execution = {'format_version': 1, 'execution_nonce': 'a' * 32,
            'source_commit': 'b' * 40, 'driver_tree': 'c' * 40,
            'artifact_id': 'app', 'environment_id': 'fedora', 'variant_id': 'gnome-wayland'}
        identity = {'pid': 123, 'parent_pid': 1, 'start_time_ticks': 42}
        self.focus = {'launch': identity, 'entry': identity, 'frame': identity,
            'frame_lineage': [dict(identity)], 'entry_lineage': [dict(identity)],
            'accessible_chain': [{'bus_name': ':1.2', 'path': '/frame', 'pid': 123}],
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
        self.sleep.assert_called_once_with(0.2)
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

    def test_both_fixed_transactions_and_partial_search_match_one_host_log(self):
        import copy, json
        actor = Mock()
        actor.observe.return_value = self.focus
        edit = guest.perform(self.execution, 'owned', actor, self.mailbox())
        search = guest.perform(self.execution, 'owned', actor, self.mailbox(), operation='editor-find-needle-v1')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            leaf = root / 'leaf/evidence/files/edit-search-review'
            leaf.mkdir(parents=True)
            selected = {'format_version': 1, 'kind': 'qemu-fixed-input', 'lease': '/root/lease', 'vm_id': 'owned'}
            def write(second, partial=False):
                observations = {'editor_keyboard': {'physical_vm_keyboard': edit}}
                observations['native_input' if partial else 'editor_search_keyboard'] = second if partial else {'physical_vm_keyboard': second}
                (leaf / 'execution.json').write_text(json.dumps({'observations': observations}))
                rows = []
                for event in edit['events'] + second['events']:
                    rows.append(protocol.canonical({'request': event['request'], 'acknowledgment': {
                        'format_version': 1, 'request_sha256': protocol.request_sha256(event['request']), 'delivery': event['delivery']}}))
                (root / 'native-input.jsonl').write_bytes(b''.join(rows))
            write(search)
            controller.verify_deliveries(root, self.execution, selected)
            partial = {**copy.deepcopy(search), 'events': search['events'][:3], 'completed': False}
            write(partial, True)
            controller.verify_deliveries(root, self.execution, selected)
            for changed in ({**partial, 'completed': True}, {**search, 'operation': 'editor-replace-save-v1'},
                            {**search, 'transaction_id': edit['transaction_id']}):
                write(changed)
                with self.assertRaises(ValueError):
                    controller.verify_deliveries(root, self.execution, selected)
            mixed = copy.deepcopy(search)
            mixed['events'][1]['request']['operation'] = 'editor-replace-save-v1'
            write(mixed)
            with self.assertRaises(ValueError):
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


    def test_reply_provider_and_prompt_transactions_match_the_independent_host_log(self):
        import copy, json
        actor = Mock()
        actor.observe.return_value = self.focus
        operations = ['provider-name-v1', 'provider-endpoint-v1-12345',
                      'provider-fixture-credential-v1', 'reply-fixture-prompt-v1']
        records = [guest.perform(self.execution, 'owned', actor, self.mailbox(), operation=operation)
                   for operation in operations]
        selected = {'format_version': 1, 'kind': 'qemu-fixed-input', 'lease': '/root/lease', 'vm_id': 'owned'}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            leaf = root / 'leaf/evidence/conversation/restart-and-reconnect'
            leaf.mkdir(parents=True)
            def write(values):
                (leaf / 'execution.json').write_text(json.dumps({'observations': {'launch_before': {'fixed_input': values}}}))
            write(records)
            host = [protocol.canonical({'request': event['request'], 'acknowledgment': {
                'format_version': 1, 'request_sha256': protocol.request_sha256(event['request']), 'delivery': event['delivery']}})
                for record in records for event in record['events']]
            (root / 'native-input.jsonl').write_bytes(b''.join(host))
            controller.verify_deliveries(root, self.execution, selected)
            for changed in (records[::-1], [records[0], {**records[1], 'operation': 'editor-find-needle-v1'}, *records[2:]],
                            [records[0], *records[2:]]):
                write(changed)
                with self.assertRaises(ValueError):
                    controller.verify_deliveries(root, self.execution, selected)
            changed = copy.deepcopy(records)
            changed[-1]['events'][-1]['focus_after']['frame_active'] = False
            write(changed)
            with self.assertRaises(ValueError):
                controller.verify_deliveries(root, self.execution, selected)

    def test_compacted_physical_input_stream_verifies_and_rejects_tampered_summary_or_stream(self):
        import copy, json
        from linux_release.installed_cases.records import compact_physical_input
        actor = Mock()
        actor.observe.return_value = self.focus
        operations = ['provider-name-v1', 'provider-endpoint-v1-12345',
                      'provider-fixture-credential-v1', 'reply-fixture-prompt-v1']
        records = [guest.perform(self.execution, 'owned', actor, self.mailbox(), operation=operation)
                   for operation in operations]
        selected = {'format_version': 1, 'kind': 'qemu-fixed-input', 'lease': '/root/lease', 'vm_id': 'owned'}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            leaf = root / 'leaf/evidence/conversation/restart-and-reconnect'
            leaf.mkdir(parents=True)
            compacted, streams = compact_physical_input({'launch_before': {'fixed_input': records}}, {})
            self.assertGreater(len(streams['physical-input']), protocol.MAX_DOCUMENT)
            (leaf / 'execution.json').write_text(json.dumps({'observations': compacted}))
            (leaf / 'physical-input.bin').write_bytes(streams['physical-input'])
            host = [protocol.canonical({'request': event['request'], 'acknowledgment': {
                'format_version': 1, 'request_sha256': protocol.request_sha256(event['request']), 'delivery': event['delivery']}})
                for record in records for event in record['events']]
            (root / 'native-input.jsonl').write_bytes(b''.join(host))
            controller.verify_deliveries(root, self.execution, selected)
            tampered_obs = copy.deepcopy(compacted)
            tampered_obs['launch_before']['fixed_input'][0]['event_count'] += 1
            (leaf / 'execution.json').write_text(json.dumps({'observations': tampered_obs}))
            with self.assertRaisesRegex(ValueError, 'summary differs'):
                controller.verify_deliveries(root, self.execution, selected)
            (leaf / 'execution.json').write_text(json.dumps({'observations': compacted}))
            (leaf / 'physical-input.bin').write_bytes(
                json.dumps({'format_version': 1, 'transactions': [records[0], records[0]]}).encode())
            with self.assertRaisesRegex(ValueError, 'duplicate transactions'):
                controller.verify_deliveries(root, self.execution, selected)
            (leaf / 'physical-input.bin').unlink()
            with self.assertRaisesRegex(ValueError, 'missing or exceeds'):
                controller.verify_deliveries(root, self.execution, selected)
            ws_leaf = root / 'leaf/evidence/windowing/workspace-activation-and-exit'
            ws_leaf.mkdir(parents=True)
            ws_transition = {'format_version': 1, 'vm_id': 'owned', 'transaction_id': 'f' * 32,
                             'operation': 'workspace-open-peer-v1', 'completed': True,
                             'events': [{'request': {}, 'delivery': {}, 'surface_after': {}}], 'text_readback': False}
            ws_compacted, ws_streams = compact_physical_input(
                {'launch': {'fixed_input': records}, 'transitions': [ws_transition]}, {})
            (ws_leaf / 'execution.json').write_text(json.dumps({'observations': ws_compacted}))
            (ws_leaf / 'physical-input.bin').write_bytes(ws_streams['physical-input'])
            (leaf / 'execution.json').unlink()
            ws_records, ws_ops = controller.delivery_records(root, self.execution, 'owned')
            self.assertEqual(ws_records, [*records, ws_transition])
            self.assertEqual(ws_ops, [*operations, 'workspace-open-peer-v1'])


if __name__ == '__main__':
    unittest.main()

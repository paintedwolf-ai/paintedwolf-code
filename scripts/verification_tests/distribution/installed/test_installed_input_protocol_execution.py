"""Fixed native input cannot accept arbitrary keys, stale steps, or unobserved delivery."""
import copy
import hashlib
import json
import unittest

from linux_release.installed_input import protocol


class InstalledInputProtocolTests(unittest.TestCase):
    def fixture(self):
        identity = {'pid': 100, 'parent_pid': 50, 'start_time_ticks': 42}
        binding = {'format_version': 1, 'execution_nonce': 'a' * 32,
                   'source_commit': 'b' * 40, 'driver_tree': 'c' * 40,
                   'artifact_id': 'native-rpm-amd64.rpm', 'environment_id': 'fedora-gnome', 'variant_id': 'fedora-wayland'}
        request = {'format_version': 1, 'binding': binding, 'transaction_id': 'd' * 32,
                   'operation': 'editor-replace-save-v1', 'ordinal': 0,
                   'focus': {'launch': identity, 'frame': dict(identity),
                             'entry': {'pid': 101, 'parent_pid': 100, 'start_time_ticks': 43},
                             'entry_focused': True, 'frame_active': True, 'frame_visible': True}}
        delivery = {'format_version': 1, 'execution_nonce': 'a' * 32, 'vm_id': 'owned-fedora',
                    'transaction_id': 'd' * 32, 'operation': request['operation'], 'ordinal': 0,
                    'event_sha256': protocol.event_sha256(request['operation'], 0),
                    'lease': {'vm_id': 'owned-fedora'}, 'qmp_ack': {'id': 3, 'return': {}}}
        ack = {'format_version': 1, 'request_sha256': protocol.request_sha256(request), 'delivery': delivery}
        return binding, request, ack

    def test_fixed_operation_has_no_text_or_key_hook_and_hashes_exact_qcodes(self):
        rows = protocol.steps('editor-replace-save-v1')
        self.assertIsInstance(rows, tuple)
        self.assertEqual(rows[0], ('ctrl', 'a'))
        self.assertEqual(rows[-1], ('ctrl', 's'))
        self.assertEqual(rows[1], ('shift', 'i'))
        expected = hashlib.sha256(json.dumps(['ctrl', 'a'], separators=(',', ':')).encode()).hexdigest()
        self.assertEqual(protocol.event_sha256('editor-replace-save-v1', 0), expected)
        for operation in ('arbitrary-text', '', {'keys': ['ctrl', 'q']}):
            with self.assertRaises(ValueError):
                protocol.steps(operation)
        for ordinal in (-1, True, len(rows), '0'):
            with self.assertRaises(ValueError):
                protocol.event_sha256('editor-replace-save-v1', ordinal)

    def test_search_operation_types_only_the_fixed_needle_and_return(self):
        rows = protocol.steps('editor-find-needle-v1')
        self.assertEqual(rows, (('ctrl', 'a'), *(('spc',) if c == ' ' else (c,) for c in 'qualification needle'), ('ret',)))
        self.assertEqual(len(rows), 22)
        self.assertNotIn(('ctrl', 's'), rows)
        binding, request, _ = self.fixture()
        request['operation'] = 'editor-find-needle-v1'
        request['text'] = 'other needle'
        with self.assertRaises(ValueError):
            protocol.request(request, binding)

    def test_embedded_entry_with_exact_active_owned_frame_is_structurally_bound(self):
        binding, request, ack = self.fixture()
        self.assertIs(protocol.request(request, binding), request)
        self.assertEqual(protocol.acknowledgment(ack, request, 'owned-fedora'), ack['delivery'])
        for field in ('entry_focused', 'frame_active', 'frame_visible'):
            changed = copy.deepcopy(request)
            changed['focus'][field] = 1
            with self.assertRaises(ValueError):
                protocol.request(changed, binding)
        changed = copy.deepcopy(request)
        changed['focus']['frame']['start_time_ticks'] += 1
        with self.assertRaises(ValueError):
            protocol.request(changed, binding)

    def test_stale_binding_mutation_extra_hooks_and_wrong_delivery_are_refused(self):
        binding, request, ack = self.fixture()
        for field, value in [('execution_nonce', 'e' * 32), ('driver_tree', 'f' * 40)]:
            changed = copy.deepcopy(request)
            changed['binding'][field] = value
            with self.assertRaises(ValueError):
                protocol.request(changed, binding)
        changed = copy.deepcopy(request)
        changed['text'] = 'caller-selected text'
        with self.assertRaises(ValueError):
            protocol.request(changed, binding)
        for field, value in [('ordinal', True), ('vm_id', 'foreign'), ('transaction_id', 'e' * 32),
                             ('event_sha256', '0' * 64)]:
            changed = copy.deepcopy(ack)
            changed['delivery'][field] = value
            with self.assertRaises(ValueError):
                protocol.acknowledgment(changed, request, 'owned-fedora')
        changed = copy.deepcopy(ack)
        changed['request_sha256'] = '0' * 64
        with self.assertRaises(ValueError):
            protocol.acknowledgment(changed, request, 'owned-fedora')

    def test_only_actual_empty_send_key_ack_and_bounded_documents_are_accepted(self):
        _, request, ack = self.fixture()
        for qmp in ({'id': True, 'return': {}}, {'id': 1, 'error': {}}, {'id': 1, 'return': {'ok': True}}):
            changed = copy.deepcopy(ack)
            changed['delivery']['qmp_ack'] = qmp
            with self.assertRaises(ValueError):
                protocol.acknowledgment(changed, request, 'owned-fedora')
        with self.assertRaises(ValueError):
            protocol.canonical({'padding': 'x' * protocol.MAX_DOCUMENT})
        with self.assertRaises(ValueError):
            protocol.canonical({'number': float('nan')})

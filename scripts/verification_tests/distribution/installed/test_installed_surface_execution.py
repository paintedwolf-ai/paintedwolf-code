"""Frame transitions retain actual object identity without weakening focused editor input."""
import copy
import unittest
from unittest.mock import Mock

from linux_release.installed_input import protocol, surface_wire, surface_guest


class SurfaceTests(unittest.TestCase):
    def setUp(self):
        self.launch = {'pid': 123, 'parent_pid': 1, 'start_time_ticks': 42}
        self.engine = {'pid': 124, 'parent_pid': 123, 'start_time_ticks': 43}
        self.main = {'bus_name': ':1.37', 'object_path': '/org/a11y/atspi/accessible/1', 'process': self.launch}
        self.peer = {**self.main, 'object_path': '/org/a11y/atspi/accessible/2'}
        self.dialog = {**self.main, 'object_path': '/org/a11y/atspi/accessible/3'}
        self.proof = {'launch': self.launch, 'engine': self.engine, 'main': self.main,
                      'surfaces': [self.row(self.main)]}
        self.execution = {'format_version': 1, 'execution_nonce': 'a' * 32,
            'source_commit': 'b' * 40, 'driver_tree': 'c' * 40,
            'artifact_id': 'app', 'environment_id': 'fedora', 'variant_id': 'gnome-wayland'}

    def row(self, identity, **changes):
        return {'identity': identity, 'kind': 'frame', 'visible': True, 'active': True,
                'defunct': False, 'modal': False, 'parent': None, **changes}

    def test_peer_creation_needs_new_object_not_repeated_activation_or_changed_runtime(self):
        operation = 'workspace-open-peer-v1'
        surface_wire.before(operation, self.proof)
        self.assertFalse(surface_wire.finished(operation, self.proof, self.proof))
        after = {**self.proof, 'surfaces': [self.row(self.main, active=False), self.row(self.peer)]}
        self.assertTrue(surface_wire.finished(operation, self.proof, after))
        for key, replacement in [('engine', {**self.engine, 'start_time_ticks': 90}),
                                 ('main', self.peer)]:
            with self.assertRaises(ValueError):
                surface_wire.finished(operation, self.proof, {**after, key: replacement})

    def test_hidden_original_must_remain_nondefunct_and_peer_must_be_visible(self):
        operation = 'workspace-hide-main-v1'
        with self.assertRaises(ValueError):
            surface_wire.before(operation, self.proof)
        before = {**self.proof, 'surfaces': [self.row(self.main), self.row(self.peer, active=False)]}
        surface_wire.before(operation, before)
        after = {**before, 'surfaces': [self.row(self.main, visible=False, active=False), self.row(self.peer)]}
        self.assertTrue(surface_wire.finished(operation, before, after))
        changed = copy.deepcopy(after)
        changed['surfaces'][0]['defunct'] = True
        self.assertFalse(surface_wire.finished(operation, before, changed))

    def test_dialog_needs_actual_parent_and_cancel_returns_original_not_replacement(self):
        dialog = self.row(self.dialog, kind='quit-dialog', modal=True, parent=self.main)
        shown = {**self.proof, 'surfaces': [self.row(self.main, active=False), dialog]}
        self.assertTrue(surface_wire.finished('workspace-request-quit-v1', self.proof, shown))
        surface_wire.before('workspace-cancel-quit-v1', shown)
        self.assertTrue(surface_wire.finished('workspace-cancel-quit-v1', shown, self.proof))
        for key, value in [('parent', None), ('modal', False)]:
            changed = copy.deepcopy(shown)
            changed['surfaces'][1][key] = value
            with self.assertRaises(ValueError):
                surface_wire.proof(changed)
        changed = {**self.proof, 'surfaces': [self.row(self.main, active=False), self.row(self.peer)]}
        self.assertFalse(surface_wire.finished('workspace-cancel-quit-v1', shown, changed))

    def test_entry_and_surface_request_authorities_cannot_be_swapped(self):
        pending = {'format_version': 1, 'binding': self.execution, 'transaction_id': 'd' * 32,
                   'operation': 'workspace-request-quit-v1', 'ordinal': 0, 'surface': self.proof}
        self.assertEqual(protocol.request(pending, self.execution), pending)
        with self.assertRaises(ValueError):
            protocol.request({**pending, 'operation': 'editor-find-needle-v1'}, self.execution)
        with self.assertRaises(ValueError):
            protocol.request({**pending, 'focus': pending['surface']}, self.execution)
        for bad in ({**self.main, 'bus_name': 'org.example.FriendlyName'},
                    {**self.main, 'object_path': 'child:1'}, {**self.main, 'process': self.engine}):
            changed = copy.deepcopy(self.proof)
            changed['surfaces'][0]['identity'] = bad
            with self.assertRaises(ValueError):
                surface_wire.proof(changed)

    def test_transition_delivery_waits_for_bound_observation_after_real_ack_and_never_replays(self):
        observer, mailbox = Mock(), Mock()
        observer.prepare.return_value = self.proof
        shown = {**self.proof, 'surfaces': [self.row(self.main, active=False),
            self.row(self.dialog, kind='quit-dialog', modal=True, parent=self.main)]}
        observer.settle.return_value = shown
        def read(_):
            pending = mailbox.write.call_args.args[1]
            return {'format_version': 1, 'request_sha256': protocol.request_sha256(pending),
                'delivery': {'format_version': 1, 'execution_nonce': 'a' * 32,
                    'vm_id': 'owned', 'transaction_id': pending['transaction_id'],
                    'operation': pending['operation'], 'ordinal': 0,
                    'event_sha256': protocol.event_sha256(pending['operation'], 0),
                    'lease': {'vm_id': 'owned'}, 'qmp_ack': {'id': 1, 'return': {}}}}
        mailbox.read.side_effect = read
        result = surface_guest.perform_transition(self.execution, 'owned', observer,
                                                  'workspace-request-quit-v1', mailbox)
        self.assertTrue(result['completed'])
        self.assertEqual(result['events'][0]['surface_after'], shown)
        observer.settle.assert_called_once()
        mailbox.clear_completed.assert_called_once()
        mailbox.reset_mock()
        observer.settle.side_effect = ValueError('captured process replaced')
        with self.assertRaises(ValueError) as refused:
            surface_guest.perform_transition(self.execution, 'owned', observer,
                                             'workspace-request-quit-v1', mailbox)
        self.assertFalse(refused.exception.observations['native_input']['completed'])
        self.assertEqual(mailbox.write.call_count, 1)
        mailbox.clear_completed.assert_not_called()

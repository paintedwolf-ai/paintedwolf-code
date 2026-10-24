"""The fixture actor identity comes from its private SSH session, never a public file."""
import copy
import io
import os
import unittest
from types import SimpleNamespace
from unittest.mock import patch

from linux_release.installed_cases import app
from linux_release.installed_input import mailbox, password_wire, protocol


class PasswordActorTests(unittest.TestCase):
    def setUp(self):
        self.execution = {'format_version': 1, 'execution_nonce': 'a' * 32,
            'source_commit': 'b' * 40, 'driver_tree': 'c' * 40,
            'artifact_id': 'app', 'environment_id': 'fedora', 'variant_id': 'gnome-wayland'}
        self.identity = {'pid': 123, 'parent_pid': 1, 'start_time_ticks': 42}
        self.actor = {'process': self.identity, 'uid': 1234, 'session_nonce': 'd' * 32}

    def test_only_exact_nonroot_process_and_fresh_session_identity_is_accepted(self):
        self.assertEqual(password_wire.actor(self.actor), self.actor)
        for change in ({'uid': 0}, {'uid': True}, {'uid': -1}, {'session_nonce': 'not-a-nonce'},
                       {'process': {**self.identity, 'start_time_ticks': 0}}, {'password': 'forbidden'}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                password_wire.actor({**copy.deepcopy(self.actor), **change})

    def test_private_secret_shape_refusal_does_not_echo_the_input(self):
        accepted = 'aB3' * 13 + '7'
        self.assertEqual(password_wire.private_secret(accepted), accepted)
        for value in ('', 'a' * 39, 'a' * 41, '!' * 40, 'é' * 40, None, {'value': accepted}):
            with self.assertRaises(ValueError) as failure:
                password_wire.private_secret(value)
            self.assertEqual(str(failure.exception), 'password fixture private frame has an invalid fixed shape')

    def serve(self, commands):
        source = io.BytesIO(b''.join(protocol.canonical(row) for row in commands))
        output = io.BytesIO()
        with patch.object(mailbox.sys, 'stdin', SimpleNamespace(buffer=source)), \
                patch.object(mailbox.sys, 'stdout', SimpleNamespace(buffer=output)), \
                patch.object(app, 'process_identity', return_value=self.identity), \
                patch.object(mailbox.os, 'getuid', return_value=1234):
            mailbox.serve(self.execution, 'owned-fedora')
        return [mailbox.decode(line) for line in output.getvalue().splitlines()]

    def test_ssh_identity_handshake_precedes_publication_and_returns_only_public_facts(self):
        command = {'operation': 'identify-password-fixture-actor', 'binding': self.execution,
                   'vm_id': 'owned-fedora'}
        rows = self.serve([command, {'operation': 'close'}])
        self.assertEqual(len(rows), 1)
        self.assertEqual(set(rows[0]), {'state', 'actor'})
        self.assertEqual(rows[0]['state'], 'password-fixture-actor')
        actor = password_wire.actor(rows[0]['actor'])
        self.assertEqual(actor['process'], self.identity)
        self.assertEqual(actor['uid'], 1234)

    def test_ssh_handshake_refuses_replay_wrong_binding_and_wrong_vm(self):
        command = {'operation': 'identify-password-fixture-actor', 'binding': self.execution,
                   'vm_id': 'owned-fedora'}
        for commands in ([command, command], [{**command, 'vm_id': 'another-vm'}],
                         [{**command, 'binding': {**self.execution, 'execution_nonce': 'f' * 32}}]):
            with self.assertRaises(ValueError):
                self.serve(commands)


class PasswordTargetTests(unittest.TestCase):
    def setUp(self):
        self.identity = {'pid': 123, 'parent_pid': 1, 'start_time_ticks': 42}
        self.engine = {'pid': 124, 'parent_pid': 123, 'start_time_ticks': 43}
        self.main = {'bus_name': ':1.37', 'object_path': '/org/a11y/atspi/accessible/1',
                     'process': self.identity, 'lineage': [self.identity]}
        self.dialog = {**self.main, 'object_path': '/org/a11y/atspi/accessible/2'}
        self.control = {**self.main, 'object_path': '/org/a11y/atspi/accessible/3'}
        self.surface = {'launch': self.identity, 'engine': self.engine, 'main': self.main, 'control': None,
            'surfaces': [self.row(self.main, active=False), self.row(self.dialog, kind='presence-dialog',
                modal=True, parent=self.main)]}
        self.native = {'kind': 'native-presence', 'surface': self.surface,
            'password': {'identity': self.control, 'parent': self.dialog, 'focused': True, 'visible': True}}
        self.execution = {'format_version': 1, 'execution_nonce': 'a' * 32,
            'source_commit': 'b' * 40, 'driver_tree': 'c' * 40,
            'artifact_id': 'app', 'environment_id': 'fedora', 'variant_id': 'gnome-wayland'}
        self.pending = {'format_version': 1, 'binding': self.execution,
            'operation': password_wire.OPERATION, 'stage': 'native-presence-password',
            'transaction_id': 'd' * 32, 'actor': {'process': self.engine, 'uid': 1234, 'session_nonce': 'e' * 32},
            'case': self.identity, 'profile': {'path': '/home/fixture/.config/paintedwolf', 'device': 1, 'inode': 2, 'uid': 1234},
            'session': {'id': '123', 'User': '1234', 'Type': 'wayland', 'Active': 'yes', 'Remote': 'no'},
            'target': self.native, 'expires_ns': 12345}

    def row(self, identity, **changes):
        return {'identity': identity, 'kind': 'frame', 'visible': True, 'active': True,
                'defunct': False, 'modal': False, 'parent': None, **changes}

    def test_native_password_requires_exact_owned_modal_parent_focused_visible_control(self):
        password_wire.target('native-presence-password', self.native)
        for key, value in [('focused', False), ('visible', False), ('parent', self.main),
                           ('identity', {**self.control, 'bus_name': ':1.99'})]:
            changed = copy.deepcopy(self.native)
            changed['password'][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                password_wire.target('native-presence-password', changed)
        changed = copy.deepcopy(self.native)
        changed['surface']['surfaces'][1]['parent'] = None
        with self.assertRaises(ValueError):
            password_wire.target('native-presence-password', changed)
        with self.assertRaises(ValueError):
            password_wire.target('new-app-password', self.native)

    def test_fixed_request_refuses_arbitrary_text_stage_profile_account_and_numeric_aliases(self):
        self.assertEqual(password_wire.request(self.pending, self.execution), self.pending)
        for changes in ({'stage': 'arbitrary-text'}, {'secret': 'must-not-be-public'},
                        {'profile': {**self.pending['profile'], 'uid': 9999}},
                        {'session': {**self.pending['session'], 'Active': 'no'}},
                        {'session': {**self.pending['session'], 'Remote': 'yes'}},
                        {'expires_ns': True}, {'binding': {**self.execution, 'execution_nonce': 'f' * 32}}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                password_wire.request({**copy.deepcopy(self.pending), **changes}, self.execution)
        # The existing normal session collector may add its independently acquired
        # GNOME owner; wire validation must not discard that runtime evidence.
        selected = copy.deepcopy(self.pending)
        selected['session']['DesktopOwner'] = {'pid': 987, 'start_time_ticks': 123}
        self.assertEqual(password_wire.request(selected, self.execution), selected)


class PrivateMailboxRoutingTests(unittest.TestCase):
    def route(self, *, identified=True, transaction=None, successful=True):
        from pathlib import Path
        import tempfile
        from unittest.mock import Mock
        from verification_tests.distribution.installed import test_installed_password_actor_execution as fixtures
        from linux_release.installed_input import password_endpoint
        fixture = fixtures.PrivateActorTests()
        fixture.setUp()
        try:
            pending = fixture.pending
            commands = []
            if identified:
                commands.append({'operation': 'identify-password-fixture-actor', 'binding': pending['binding'],
                                 'vm_id': 'owned-fedora'})
            commands += [{'operation': 'poll'}, {'operation': 'open-password-fixture',
                'transaction_id': transaction or pending['transaction_id']}, {'operation': 'poll'}, {'operation': 'close'}]
            source = io.BytesIO(b''.join(protocol.canonical(row) for row in commands))
            output = io.BytesIO()
            with tempfile.TemporaryDirectory() as directory:
                path = Path(directory)
                (path / 'binding.json').write_text('{}')
                selected = SimpleNamespace(path=path, attach=Mock(), read=Mock(side_effect=lambda name:
                    pending if name == 'request.json' else {} if name == 'binding.json' else None))
                with patch.object(mailbox, 'Mailbox', return_value=selected), \
                     patch.object(mailbox.sys, 'stdin', SimpleNamespace(buffer=source)), \
                     patch.object(mailbox.sys, 'stdout', SimpleNamespace(buffer=output)), \
                     patch.object(app, 'process_identity', return_value=pending['actor']['process']), \
                     patch.object(mailbox.os, 'getuid', return_value=1234), \
                     patch.object(mailbox.secrets, 'token_hex', return_value=pending['actor']['session_nonce']), \
                     patch.object(password_endpoint, 'serve', return_value=successful) as endpoint:
                    mailbox.serve(pending['binding'], 'owned-fedora')
                    self.assertEqual(endpoint.call_count, 1)
                    self.assertEqual(endpoint.call_args.args, (selected, pending, pending['actor']))
            return [mailbox.decode(line) for line in output.getvalue().splitlines()]
        finally:
            fixture.doCleanups()

    def test_private_pending_uses_only_the_closed_route_and_is_acknowledged_once(self):
        rows = self.route()
        self.assertEqual(rows[1]['state'], 'private-request')
        self.assertEqual(rows[2], {'state': 'acknowledged'})
        self.assertNotIn('secret', str(rows))

    def test_missing_actor_or_changed_transaction_refuses_before_the_private_endpoint(self):
        with self.assertRaises(ValueError):
            self.route(identified=False)
        with self.assertRaises(ValueError):
            self.route(transaction='f' * 32)

    def test_failed_private_endpoint_exits_without_printing_exception_details(self):
        with self.assertRaises(SystemExit) as failure:
            self.route(successful=False)
        self.assertEqual(failure.exception.code, 1)

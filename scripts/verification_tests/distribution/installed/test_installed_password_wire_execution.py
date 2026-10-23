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

"""All three private roles exchange real bounded pipes and kernel-checked Unix frames."""
import copy
import os
from pathlib import Path
import selectors
import tempfile
import threading
import time
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_process import process_identity
from linux_release.installed_input import password_endpoint, password_exchange, private_pipe, password_socket
from verification_tests.distribution.installed import test_installed_password_actor_execution as actor_tests


@unittest.skipUnless(os.name == 'posix' and hasattr(os, 'unshare'), 'private peer fixture requires Linux CLONE_FS')
class PasswordTransportTests(unittest.TestCase):
    setUp = actor_tests.PrivateActorTests.setUp

    def transport(self):
        to_endpoint_read, to_endpoint_write = os.pipe()
        to_host_read, to_host_write = os.pipe()
        host = SimpleNamespace(stdin=os.fdopen(to_endpoint_write, 'wb', buffering=0),
                               stdout=os.fdopen(to_host_read, 'rb', buffering=0))
        endpoint = SimpleNamespace(stdin=os.fdopen(to_host_write, 'wb', buffering=0),
                                   stdout=os.fdopen(to_endpoint_read, 'rb', buffering=0))
        deadline = time.monotonic() + 5
        result = []
        for process in (host, endpoint):
            selector = selectors.DefaultSelector()
            self.addCleanup(selector.close)
            for stream in (process.stdin, process.stdout):
                os.set_blocking(stream.fileno(), False)
                self.addCleanup(stream.close)
            selector.register(process.stdout, selectors.EVENT_READ)
            result.append(private_pipe.Pipe(process, selector, deadline))
        return result

    def exercise(self, changed=False):
        host, endpoint = self.transport()
        with tempfile.TemporaryDirectory() as root:
            directory = Path(root)
            directory.chmod(0o700)
            profile = directory / 'profile'
            profile.mkdir(mode=0o700)
            identity = process_identity(os.getpid())
            self.pending.update(actor={'process': identity, 'uid': os.getuid(), 'session_nonce': 'e' * 32},
                                case=identity, expires_ns=time.monotonic_ns() + 5_000_000_000)
            self.pending['profile'] = {'path': str(profile), 'device': profile.stat().st_dev,
                                      'inode': profile.stat().st_ino, 'uid': os.getuid()}
            self.pending['session']['User'] = str(os.getuid())
            mailbox = SimpleNamespace(path=directory, execution=self.pending['binding'], vm_id='owned-test',
                                      attach=Mock(), _directory=directory.lstat)
            ready, errors, facts = threading.Event(), [], []
            focus = Mock()
            focus.observe.return_value = self.pending['target']
            if changed:
                altered = copy.deepcopy(self.pending['target'])
                altered['label'] = 'Confirm app password'
                focus.observe.side_effect = [self.pending['target']] * 2 + [altered]
            def case():
                try:
                    with password_socket.Listener(mailbox, self.pending['actor']) as listener:
                        ready.set()
                        channel = listener.accept(host.deadline)
                        facts.append(password_exchange.witness(channel, self.pending, self.secret, focus, mailbox.vm_id))
                except BaseException as error:
                    errors.append(('case', type(error).__name__))
                    ready.set()
            def ssh_endpoint():
                try:
                    password_endpoint.relay(mailbox, self.pending, self.pending['actor'], endpoint)
                except BaseException as error:
                    errors.append(('endpoint', type(error).__name__))
                finally:
                    endpoint.process.stdin.close()
                    endpoint.process.stdout.close()
            case_thread = threading.Thread(target=case)
            case_thread.start()
            self.assertTrue(ready.wait(3))
            endpoint_thread = threading.Thread(target=ssh_endpoint)
            with patch.object(password_endpoint, 'graphical_session', return_value=self.pending['session']):
                endpoint_thread.start()
                try:
                    if changed:
                        with self.assertRaises(ValueError) as failure:
                            password_exchange.exchange(self.actor, self.pending, host)
                        result = failure.exception.private_fixture
                    else:
                        result = password_exchange.exchange(self.actor, self.pending, host)
                finally:
                    host.process.stdin.close()
                    host.process.stdout.close()
                    endpoint_thread.join(6)
                    case_thread.join(6)
            self.assertFalse(case_thread.is_alive())
            self.assertFalse(endpoint_thread.is_alive())
            self.assertFalse((directory / password_socket.NAME).exists())
            return result, facts, errors

    def test_complete_private_chain_agrees_on_aggregate_without_retaining_key_frames(self):
        result, facts, errors = self.exercise()
        self.assertEqual(errors, [])
        self.assertEqual(facts, [result])
        self.assertTrue(result['guest_confirmed'])
        self.assertEqual(result['delivery']['acknowledged_count'], 41)
        self.assertNotIn(self.secret, str(result))
        self.assertNotIn('event_sha256', str(result))
        self.assertNotIn('qmp_ack', str(result))

    def test_actual_target_change_closes_the_chain_without_replaying_a_physical_key(self):
        result, facts, errors = self.exercise(changed=True)
        self.assertEqual(facts, [])
        self.assertEqual({role for role, _ in errors}, {'case', 'endpoint'})
        self.assertFalse(result['guest_confirmed'])
        self.assertEqual(result['delivery']['acknowledged_count'], 1)
        self.assertTrue(self.actor.failed)
        self.assertEqual(self.actor.command.call_count, 2)

    def test_expired_guest_clock_refuses_before_connecting_or_transmitting_bytes(self):
        identity = process_identity(os.getpid())
        self.pending.update(actor={'process': identity, 'uid': os.getuid(), 'session_nonce': 'e' * 32},
                            case=identity, expires_ns=1)
        self.pending['profile']['uid'] = os.getuid()
        self.pending['session']['User'] = str(os.getuid())
        mailbox = SimpleNamespace(execution=self.pending['binding'])
        pipe = Mock()
        info = self.pending['profile']
        with patch.object(password_endpoint, 'physical', return_value=(info['device'], info['inode'], info['uid'], 0o40700)), \
             patch.object(password_endpoint, 'graphical_session', return_value=self.pending['session']), \
             patch.object(password_endpoint, 'connect') as connect:
            with self.assertRaises(ValueError):
                password_endpoint.relay(mailbox, self.pending, self.pending['actor'], pipe)
        connect.assert_not_called()
        pipe.send_frame.assert_not_called()

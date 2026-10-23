"""Private SSH pipes retain one deadline across partial reads and blocked writes."""
import os
import selectors
import struct
import sys
import threading
import time
from types import SimpleNamespace
import unittest

from linux_release.installed_input import password_pipe, password_wire, protocol
from linux_release.installed_input.mailbox import decode


@unittest.skipUnless(sys.platform == 'linux', 'the QEMU controller owns Linux SSH pipe endpoints')
class PasswordPipeTests(unittest.TestCase):
    def setUp(self):
        incoming, to_peer = os.pipe()
        from_peer, outgoing = os.pipe()
        self.input = os.fdopen(to_peer, 'wb', buffering=0)
        self.output = os.fdopen(from_peer, 'rb', buffering=0)
        self.peer_read = os.fdopen(incoming, 'rb', buffering=0)
        self.peer_write = os.fdopen(outgoing, 'wb', buffering=0)
        for stream in (self.input, self.output, self.peer_read, self.peer_write):
            self.addCleanup(stream.close)
        os.set_blocking(self.input.fileno(), False)
        os.set_blocking(self.output.fileno(), False)
        self.selector = selectors.DefaultSelector()
        self.addCleanup(self.selector.close)
        self.selector.register(self.output, selectors.EVENT_READ)
        self.pipe = password_pipe.Pipe(SimpleNamespace(stdin=self.input, stdout=self.output),
                                       self.selector, time.monotonic() + 2)

    def test_private_value_is_a_separate_bounded_frame_and_not_a_metadata_payload(self):
        secret, observed = 'aB3' * 13 + '7', []
        def exact(count):
            raw = bytearray()
            while len(raw) < count:
                block = self.peer_read.read(count - len(raw))
                if not block:
                    raise ValueError('fixture peer ended before its exact frame')
                raw.extend(block)
            return bytes(raw)
        def endpoint():
            size, = struct.unpack('!I', exact(4))
            observed.append(decode(exact(size)))
            self.peer_write.write(password_wire.frame(secret.encode(), 128))
            self.peer_write.write(password_wire.frame(protocol.canonical({'operation': 'ready'}), protocol.MAX_DOCUMENT))
        worker = threading.Thread(target=endpoint)
        worker.start()
        try:
            self.pipe.send_metadata({'operation': 'fixed-fixture-open', 'transaction_id': 'a' * 32})
            self.assertEqual(self.pipe.receive_fixture_value(), secret)
            self.assertEqual(self.pipe.receive_metadata(), {'operation': 'ready'})
        finally:
            worker.join(3)
        self.assertFalse(worker.is_alive())
        self.assertEqual(observed, [{'operation': 'fixed-fixture-open', 'transaction_id': 'a' * 32}])
        self.assertNotIn(secret, str(observed))

    def test_full_owned_write_pipe_cannot_block_past_the_fixed_deadline(self):
        while True:
            try:
                os.write(self.input.fileno(), b'x' * 4096)
            except BlockingIOError:
                break
        self.pipe.deadline = time.monotonic() + 0.05
        started = time.monotonic()
        with self.assertRaises(TimeoutError):
            self.pipe.write(b'fixed fixture metadata')
        self.assertLess(time.monotonic() - started, 1)

    def test_partial_bytes_never_extend_the_complete_frame_deadline(self):
        stop = threading.Event()
        def incomplete():
            self.peer_write.write(struct.pack('!I', 40))
            while not stop.wait(0.01):
                self.peer_write.write(b'a')
        worker = threading.Thread(target=incomplete)
        self.pipe.deadline = time.monotonic() + 0.05
        started = time.monotonic()
        worker.start()
        try:
            with self.assertRaises(TimeoutError):
                self.pipe.receive_fixture_value()
            self.assertLess(time.monotonic() - started, 1)
        finally:
            stop.set()
            worker.join(2)
        self.assertFalse(worker.is_alive())

    def test_oversized_or_truncated_frame_and_invalid_value_do_not_echo_bytes(self):
        self.peer_write.write(struct.pack('!I', 129))
        with self.assertRaises(ValueError):
            self.pipe.receive_fixture_value()
        self.peer_write.write(struct.pack('!I', 40) + b'!' * 40)
        with self.assertRaises(ValueError) as failure:
            self.pipe.receive_fixture_value()
        self.assertEqual(str(failure.exception), 'private actor value has an invalid fixed shape')
        self.peer_write.write(b'\x00\x00')
        self.peer_write.close()
        with self.assertRaises(ValueError):
            self.pipe.receive_metadata()

    def test_normal_public_exchange_keeps_the_same_bounded_transport(self):
        observed = []
        def endpoint():
            observed.append(decode(self.peer_read.readline(protocol.MAX_DOCUMENT + 1)))
            self.peer_write.write(protocol.canonical({'state': 'waiting'}))
        worker = threading.Thread(target=endpoint)
        worker.start()
        try:
            self.assertEqual(self.pipe.line({'operation': 'poll'}), {'state': 'waiting'})
        finally:
            worker.join(3)
        self.assertFalse(worker.is_alive())
        self.assertEqual(observed, [{'operation': 'poll'}])

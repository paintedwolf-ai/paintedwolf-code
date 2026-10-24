"""Private SSH pipes retain one deadline across partial reads and blocked writes."""
import os
import selectors
import struct
import sys
import threading
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.installed_input import private_pipe, protocol
from linux_release.installed_input.mailbox import decode


@unittest.skipUnless(sys.platform == 'linux', 'the QEMU controller owns Linux SSH pipe endpoints')
class PrivatePipeTests(unittest.TestCase):
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
        self.pipe = private_pipe.Pipe(SimpleNamespace(stdin=self.input, stdout=self.output),
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
            self.peer_write.write(private_pipe.frame(secret.encode(), 128))
            self.peer_write.write(private_pipe.frame(protocol.canonical({'operation': 'ready'}), protocol.MAX_DOCUMENT))
        worker = threading.Thread(target=endpoint)
        worker.start()
        try:
            self.pipe.send_metadata({'operation': 'fixed-fixture-open', 'transaction_id': 'a' * 32})
            self.assertEqual(self.pipe.receive_frame(128), secret.encode())
            self.assertEqual(self.pipe.receive_metadata(), {'operation': 'ready'})
        finally:
            worker.join(3)
        self.assertFalse(worker.is_alive())
        self.assertEqual(observed, [{'operation': 'fixed-fixture-open', 'transaction_id': 'a' * 32}])
        self.assertNotIn(secret, str(observed))

    def test_send_frame_writes_exact_opaque_payload_without_metadata_encoding(self):
        raw = b'\xff\x00!' * 10
        self.pipe.send_frame(raw, 128)
        self.assertEqual(self.peer_read.read(len(raw) + 4), private_pipe.frame(raw, 128))

    def test_blocking_owned_endpoints_are_refused_before_any_transfer(self):
        for stream in (self.input, self.output):
            with self.subTest(endpoint=stream.fileno()):
                os.set_blocking(stream.fileno(), True)
                try:
                    with self.assertRaises(ValueError):
                        private_pipe.Pipe(self.pipe.process, self.selector, self.pipe.deadline)
                finally:
                    os.set_blocking(stream.fileno(), False)

    def test_full_owned_write_pipe_uses_only_the_original_remaining_deadline(self):
        while True:
            try:
                os.write(self.input.fileno(), b'x' * 4096)
            except BlockingIOError:
                break
        self.pipe.deadline = 101
        actual_select = private_pipe.select.select
        def ready(readers, writers, errors, timeout):
            return actual_select(readers, writers, errors, 0)
        with patch.object(private_pipe.time, 'monotonic', return_value=100), \
                patch.object(private_pipe.select, 'select', side_effect=ready) as wait:
            with self.assertRaises(TimeoutError):
                self.pipe.write(b'fixed fixture metadata')
        wait.assert_called_once_with([], [self.input.fileno()], [], 1)
        self.assertEqual(self.pipe.deadline, 101)

    def test_partial_bytes_never_extend_the_complete_frame_deadline(self):
        self.peer_write.write(struct.pack('!I', 40) + b'a')
        self.pipe.deadline = 101
        with patch.object(private_pipe.time, 'monotonic', side_effect=[100, 100.25, 101]), \
                patch.object(self.selector, 'select', wraps=self.selector.select) as wait:
            with self.assertRaises(TimeoutError):
                self.pipe.receive_frame(128)
        self.assertEqual([call.args[0] for call in wait.call_args_list], [1, 0.75])
        self.assertEqual(self.pipe.deadline, 101)

    def test_oversized_or_truncated_frame_refuses_but_payload_grammar_is_opaque(self):
        self.peer_write.write(struct.pack('!I', 129))
        with self.assertRaises(ValueError):
            self.pipe.receive_frame(128)
        self.peer_write.write(struct.pack('!I', 40) + b'!' * 40)
        self.assertEqual(self.pipe.receive_frame(128), b'!' * 40)
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


class PrivateFrameTests(unittest.TestCase):
    def test_frame_codec_keeps_opaque_bytes_and_exact_length(self):
        raw = b'\xff\x00!'
        encoded = private_pipe.frame(raw, 3)
        self.assertEqual(encoded[4:], raw)
        self.assertEqual(private_pipe.frame_size(encoded[:4], 3), 3)

    def test_frame_codec_refuses_empty_oversized_and_incomplete_headers(self):
        for raw in (b'', b'1234', 'abc'):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                private_pipe.frame(raw, 3)
        for header in (b'', b'123', b'12345', struct.pack('!I', 0), struct.pack('!I', 4)):
            with self.subTest(header=header), self.assertRaises(ValueError):
                private_pipe.frame_size(header, 3)

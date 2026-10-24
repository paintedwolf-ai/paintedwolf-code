"""Bounded I/O on the controller's owned private SSH pipes."""
from __future__ import annotations

import os
import select
import struct
import time

from . import protocol


def frame(raw, maximum):
    if not isinstance(raw, bytes) or not 0 < len(raw) <= maximum:
        raise ValueError('private fixture frame exceeded its fixed bound')
    return struct.pack('!I', len(raw)) + raw


def frame_size(header, maximum):
    if not isinstance(header, bytes) or len(header) != 4:
        raise ValueError('private fixture frame has no complete bounded header')
    size, = struct.unpack('!I', header)
    if not 0 < size <= maximum:
        raise ValueError('private fixture frame exceeded its fixed bound')
    return size


class Pipe:
    def __init__(self, process, selector, deadline):
        if os.get_blocking(process.stdin.fileno()) or os.get_blocking(process.stdout.fileno()):
            raise ValueError('private actor transport requires nonblocking owned pipe endpoints')
        self.process, self.selector, self.deadline = process, selector, deadline

    def remaining(self):
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError('private actor transport exceeded its fixed deadline')
        return remaining

    def write(self, raw):
        offset, descriptor = 0, self.process.stdin.fileno()
        while offset < len(raw):
            if not select.select([], [descriptor], [], self.remaining())[1]:
                raise TimeoutError('private actor transport write exceeded its fixed deadline')
            try:
                written = os.write(descriptor, raw[offset:])
            except (BlockingIOError, InterruptedError):
                continue
            if written <= 0:
                raise ValueError('private actor transport ended during its bounded write')
            offset += written

    def read(self, count):
        raw = bytearray()
        while len(raw) < count:
            if not self.selector.select(self.remaining()):
                raise TimeoutError('private actor transport read exceeded its fixed deadline')
            try:
                block = os.read(self.process.stdout.fileno(), count - len(raw))
            except (BlockingIOError, InterruptedError):
                continue
            if not block:
                raise ValueError('private actor transport ended before completion')
            raw.extend(block)
        return bytes(raw)

    def line(self, document):
        from .mailbox import decode
        self.write(protocol.canonical(document))
        raw = bytearray()
        while len(raw) <= protocol.MAX_DOCUMENT:
            raw.extend(self.read(1))
            if raw[-1] == 10:
                return decode(bytes(raw))
        raise ValueError('private actor line exceeded its fixed bound')

    def send_metadata(self, document):
        self.write(frame(protocol.canonical(document), protocol.MAX_DOCUMENT))

    def receive_metadata(self):
        from .mailbox import decode
        size = frame_size(self.read(4), protocol.MAX_DOCUMENT)
        try:
            return decode(self.read(size))
        except (ValueError, UnicodeError):
            raise ValueError('private actor metadata has an invalid fixed shape') from None

    def send_frame(self, raw, maximum):
        self.write(frame(raw, maximum))

    def receive_frame(self, maximum):
        return self.read(frame_size(self.read(4), maximum))

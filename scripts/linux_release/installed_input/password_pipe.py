"""Bounded I/O on the controller's owned private SSH pipes."""
from __future__ import annotations

import os
import select
import time

from . import password_wire, protocol


class Pipe:
    def __init__(self, process, selector, deadline):
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
        self.write(password_wire.frame(protocol.canonical(document), protocol.MAX_DOCUMENT))

    def receive_metadata(self):
        from .mailbox import decode
        size = password_wire.frame_size(self.read(4), protocol.MAX_DOCUMENT)
        try:
            return decode(self.read(size))
        except (ValueError, UnicodeError):
            raise ValueError('private actor metadata has an invalid fixed shape') from None

    def send_fixture_value(self, value):
        self.write(password_wire.frame(password_wire.private_secret(value).encode('ascii'), 128))

    def receive_fixture_value(self):
        size = password_wire.frame_size(self.read(4), 128)
        try:
            return password_wire.private_secret(self.read(size).decode('ascii'))
        except (ValueError, UnicodeError):
            raise ValueError('private actor value has an invalid fixed shape') from None

"""Bound one real fixture response to observable transport release or peer cancellation."""
from __future__ import annotations

import hashlib
import os
import secrets
import select
import socket
import threading
import time

from .records import CaseUnavailable


class ResponseBarrier:
    def __init__(self, timeout_seconds=120):
        if (type(timeout_seconds) not in {int, float} or not 0 < timeout_seconds <= 180):
            raise ValueError('fixture response hold requires a bounded positive deadline')
        self.timeout = timeout_seconds
        self.lock = threading.Lock()
        self.released = threading.Event()
        self.done = threading.Event()
        self.settled = threading.Event()
        self.record = None
        self.connection = None
        self.cleanup = False

    def capture(self, connection, request):
        with self.lock:
            if self.cleanup:
                raise ValueError('fixture response hold is closed')
            if self.record is not None:
                if not self.done.is_set():
                    raise ValueError('fixture response hold received concurrent model requests')
                return None
            held = connection.dup()
            info = os.fstat(held.fileno())
            now = time.monotonic()
            lease = secrets.token_hex(16)
            self.connection = held
            self.record = {'request_id': lease, 'request_sha256': hashlib.sha256(request).hexdigest(),
                'socket': {'device': info.st_dev, 'inode': info.st_ino},
                'received_at': now, 'deadline': now + self.timeout,
                'state': 'held', 'connection_closed': False, 'response_written': False}
            return lease

    def _cancel(self, reason, closed=False):
        self.record.update(state='cancelled', cancellation=reason,
                           connection_closed=closed, finished_at=time.monotonic())
        self.done.set()

    def wait(self, lease):
        with self.lock:
            if self.record is None or self.record['request_id'] != lease:
                raise ValueError('fixture response lease differs from its captured request')
            connection = self.connection
        poll = select.poll()
        poll.register(connection, select.POLLIN | select.POLLHUP | select.POLLERR | select.POLLRDHUP)
        while True:
            events = poll.poll(20)
            closed = any(flags & (select.POLLHUP | select.POLLERR | select.POLLRDHUP) for _, flags in events)
            if events and not closed:
                # Request bytes were fully consumed before capture. Additional
                # input is not another request on this held response connection.
                closed = connection.recv(1, socket.MSG_PEEK) == b''
                if not closed:
                    with self.lock:
                        self._cancel('unexpected-connection-input')
                    return False
            with self.lock:
                info = os.fstat(connection.fileno())
                if (info.st_dev, info.st_ino) != (self.record['socket']['device'], self.record['socket']['inode']):
                    self._cancel('connection-identity-changed')
                    return False
                if self.record['state'] == 'cancelled':
                    return False
                if self.cleanup:
                    self._cancel('fixture-cleanup')
                    return False
                if closed:
                    self._cancel('peer-closed', True)
                    return False
                if time.monotonic() >= self.record['deadline']:
                    self._cancel('deadline-expired')
                    return False
                if self.released.is_set():
                    self.record.update(state='released', released_at=time.monotonic())
                    return True

    def finish(self, lease, written):
        with self.lock:
            if self.record is None or self.record['request_id'] != lease:
                raise ValueError('fixture completion differs from its response lease')
            if self.record['state'] == 'released' and written:
                self.record.update(response_written=True, finished_at=time.monotonic())
                self.done.set()
            elif self.record['state'] != 'cancelled':
                self._cancel('response-write-failed')
            self.connection.close()
            self.connection = None
            self.settled.set()

    def snapshot(self):
        with self.lock:
            if self.record is None:
                return None
            return {**self.record, 'socket': dict(self.record['socket'])}

    def require_held(self):
        with self.lock:
            row = self.record
            if self.cleanup or row is None or row['state'] != 'held' or time.monotonic() >= row['deadline']:
                raise CaseUnavailable('fixed-provider-request-is-not-independently-held')
            info = os.fstat(self.connection.fileno())
            if (info.st_dev, info.st_ino) != (row['socket']['device'], row['socket']['inode']):
                self._cancel('connection-identity-changed')
                raise CaseUnavailable('fixed-provider-request-connection-identity-changed')
            poll = select.poll()
            poll.register(self.connection, select.POLLIN | select.POLLHUP | select.POLLERR | select.POLLRDHUP)
            events = poll.poll(0)
            if events:
                closed = any(flags & (select.POLLHUP | select.POLLERR | select.POLLRDHUP) for _, flags in events)
                if not closed:
                    closed = self.connection.recv(1, socket.MSG_PEEK) == b''
                self._cancel('peer-closed' if closed else 'unexpected-connection-input', closed)
                raise CaseUnavailable('fixed-provider-request-lost-its-held-connection')
            return {**row, 'socket': dict(row['socket'])}

    def await_held(self, timeout=15):
        deadline = time.monotonic() + timeout
        while self.snapshot() is None and time.monotonic() < deadline:
            time.sleep(0.02)
        return self.require_held()

    def release(self):
        self.require_held()
        self.released.set()

    def await_response_written(self, timeout=10):
        if not self.done.wait(timeout):
            raise CaseUnavailable('fixed-provider-response-was-not-written-before-its-deadline')
        row = self.snapshot()
        if row is None or row['state'] != 'released' or row['response_written'] is not True:
            raise CaseUnavailable('fixed-provider-request-ended-without-its-controlled-response')
        return row

    def await_peer_cancellation(self, timeout=10):
        if not self.done.wait(timeout):
            raise CaseUnavailable('fixed-provider-request-did-not-observe-peer-cancellation')
        row = self.snapshot()
        if row is None or row['state'] != 'cancelled' or row.get('cancellation') != 'peer-closed' or row['connection_closed'] is not True:
            raise CaseUnavailable('fixed-provider-request-ended-without-peer-cancellation')
        return row

    def close(self):
        with self.lock:
            self.cleanup = True
            captured = self.record is not None
            if self.connection is not None:
                try:
                    self.connection.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass
        if captured and not self.settled.wait(5):
            raise RuntimeError('fixture response hold did not settle during cleanup')

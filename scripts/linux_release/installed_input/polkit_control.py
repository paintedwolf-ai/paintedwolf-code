"""Closed transaction lifecycle for actual Polkit observation and one-use OS input."""
from __future__ import annotations

from copy import deepcopy
from pathlib import Path
import time
import threading

from . import polkit_capture, protocol, session_wire
from .polkit_account import Credential
from .polkit_proxy import Proxy
from .polkit_stage import CANCEL, PURPOSE, Stage
from .polkit_wire import validate
from .unlock_account import validate_account_shape

START = 'native-polkit-start-v1'
BIND = 'native-polkit-bind-v1'
OBSERVE = 'native-polkit-observe-v1'
CLOSE = 'native-polkit-close-v1'
FIELDS = {START: {'native_session', 'runtime'}, BIND: {'attempt'}, OBSERVE: set(),
          PURPOSE: set(), CANCEL: set(), CLOSE: set()}
OPERATIONS = frozenset(FIELDS)
BASE = {'format_version', 'binding', 'transaction_id', 'session_id', 'operation', 'account'}


def request(value, execution):
    operation = value.get('operation') if isinstance(value, dict) else None
    if (operation not in FIELDS or set(value) != BASE | FIELDS[operation]
            or type(value['format_version']) is not int or value['format_version'] != 1
            or protocol.binding(value['binding']) != protocol.binding(execution)):
        raise ValueError('Polkit control requires its closed current execution request')
    protocol.nonce(value['transaction_id'])
    protocol.nonce(value['session_id'])
    validate_account_shape(value['account'])
    if operation == START:
        native = session_wire.native_session(value['native_session'])
        if (native['session']['User'] != str(value['account']['uid'])
                or native['locked_hint'] or native['locker_active']
                or not isinstance(value['runtime'], dict)):
            raise ValueError('Polkit start requires its exact unlocked marked account and runtime')
    if operation == BIND:
        attempt = validate(value['attempt'])
        if (attempt['phase'] != 'interactive' or attempt['transport'] != 'pending'
                or attempt['subject']['uid'] != value['account']['uid']):
            raise ValueError('Polkit binding requires its actual pending marked subject')
    protocol.canonical(value)
    return value


def acknowledgment(value, pending, vm_id):
    request(pending, pending['binding'])
    if (not isinstance(value, dict) or set(value) != {'format_version', 'request_sha256', 'vm_id', 'result'}
            or type(value['format_version']) is not int or value['format_version'] != 1
            or value['request_sha256'] != protocol.request_sha256(pending) or value['vm_id'] != vm_id
            or not isinstance(value['result'], dict)):
        raise ValueError('Polkit acknowledgment differs from its selected request')
    result = value['result']
    operation = pending['operation']
    if operation == START and (set(result) != {'monitoring', 'capture'}
            or result['monitoring'] is not True or not isinstance(result['capture'], dict)):
        raise ValueError('Polkit start has no captured observer prerequisite')
    if operation == CLOSE and result != {'closed': True, 'credential_disabled': True}:
        raise ValueError('Polkit close has no completed credential cleanup')
    if operation in {PURPOSE, CANCEL} and result != {'purpose': operation,
            'transaction_id': pending['transaction_id'], 'delivered': True,
            'text_readback': False, 'host_release_verified': False}:
        raise ValueError('Polkit delivery cannot claim authentication or host release')
    if operation in {BIND, OBSERVE} and set(result) != {'observation'}:
        raise ValueError('Polkit observation has an unknown result shape')
    protocol.canonical(value)
    return value


class Session:
    """Own cleanup independently of a failed monitor, transport or physical event."""
    def __init__(self, actor, target, execution, *, checkout=None):
        self.actor, self.target, self.execution = actor, target, protocol.binding(execution)
        self.checkout = Path(checkout) if checkout is not None else Path(__file__).resolve().parents[3]
        self.identity = self.credential = self.observer = self.stage = None
        self.lock = threading.RLock()
        self.transactions = set()
        self.failed = self.closed = self.bound = False

    def handle(self, pending, deliver):
        with self.lock:
            return self._handle(pending, deliver)

    def _live(self):
        if self.actor.failed or self.actor.closed:
            raise ValueError('Polkit lifecycle lost its physical actor custody')

    def _handle(self, pending, deliver):
        request(pending, self.execution)
        identity = {key: deepcopy(pending[key]) for key in ('session_id', 'account')}
        if pending['transaction_id'] in self.transactions or len(self.transactions) >= 128:
            raise ValueError('Polkit lifecycle cannot replay or exceed its request bound')
        self.transactions.add(pending['transaction_id'])
        operation = pending['operation']
        if self.closed or self.failed and operation != CLOSE:
            raise ValueError('Polkit lifecycle cannot resume uncertain custody')
        if self.identity is not None and self.identity != identity:
            raise ValueError('Polkit lifecycle replaced its selected account or session')
        if operation != START and self.identity is None:
            raise ValueError('Polkit lifecycle requires its initial observer capture')
        try:
            if operation != CLOSE:
                self._live()
            if operation == START:
                if self.identity is not None:
                    raise ValueError('Polkit lifecycle cannot restart its observer')
                self.identity = identity
                capture = polkit_capture.prepare(self.actor, self.target, self.execution, self.checkout)
                self._live()
                self.credential = Credential(self.actor, self.target, self.execution, identity['account'])
                self.credential.prepare()
                self._live()
                self.observer = Proxy(self.actor, self.target, self.execution, identity['account'])
                session = {**pending['native_session']['session'], 'Active': 'yes', 'Remote': 'no', 'Desktop': 'GNOME'}
                self.observer.start(session, pending['runtime'])
                return {'monitoring': True, 'capture': capture}
            if operation == CLOSE:
                self.close()
                return {'closed': True, 'credential_disabled': True}
            if operation == BIND:
                if self.bound:
                    raise ValueError('Polkit lifecycle cannot rebind a selected call')
                self.bound = True
                return {'observation': self.observer.bind(pending['attempt'])}
            if not self.bound:
                raise ValueError('Polkit lifecycle requires its actual native call selection')
            if operation == OBSERVE:
                return {'observation': self.observer.observe()}
            if self.stage is not None:
                raise ValueError('Polkit lifecycle cannot repeat authenticate or cancel delivery')
            secret = self.credential.take() if operation == PURPOSE else None
            try:
                self.stage = Stage(self.actor, self.execution, identity['account'], secret,
                                   observer=self.observer, purpose=operation)
            finally:
                if secret is not None:
                    secret[:] = b'\0' * len(secret)
            return deliver(self.stage, pending, time.monotonic() + 30)
        except BaseException:
            self.failed = True
            self.close()
            raise

    def close(self):
        with self.lock:
            return self._close()

    def _close(self):
        if self.closed:
            return
        self.closed = True
        try:
            if self.stage is not None:
                self.stage.close()
        finally:
            try:
                if self.observer is not None:
                    self.observer.close()
            finally:
                if self.credential is not None:
                    self.credential.close()

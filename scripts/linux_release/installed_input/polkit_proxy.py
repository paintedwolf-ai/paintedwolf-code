"""The physical actor's private, lease-bound transport to its guest root observer."""
from __future__ import annotations

import os
from copy import deepcopy
import selectors
import subprocess
import time

from . import protocol
from .polkit_wire import validate
from .private_pipe import Pipe
from .unlock_account import validate_account_shape
from ..installed_process import process_identity

from .captured_driver import OBSERVER_BOOTSTRAP


class Proxy:
    def __init__(self, actor, target, execution, account):
        self.execution = deepcopy(protocol.binding(execution))
        self.account = dict(validate_account_shape(account))
        self.vm_id = actor.value['vm_id']
        if (actor.nonce != self.execution['execution_nonce']
                or actor.value['ssh'] != {key: target[key] for key in ('host', 'port', 'user')}):
            raise ValueError('Polkit root observer differs from the physical actor guest lease')
        self.sequence = 0
        self.started = self.bound = self.failed = self.closed = False
        self.deadline = time.monotonic() + 150
        self.process = None
        self.selector = selectors.DefaultSelector()
        try:
            from ..installed_campaign import command
            arguments = ['/usr/bin/sudo', '-n', '/usr/bin/env', '-i', 'PATH=/usr/bin:/bin',
                         'LANG=C.UTF-8', 'HOME=/root', '/usr/bin/python3', '-I', '-B', '-c', OBSERVER_BOOTSTRAP,
                         self.execution['driver_tree']]
            self.process = subprocess.Popen(command(target, arguments), stdin=subprocess.PIPE,
                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, start_new_session=True, bufsize=0)
            self.process_birth = process_identity(self.process.pid)
            os.set_blocking(self.process.stdin.fileno(), False)
            os.set_blocking(self.process.stdout.fileno(), False)
            self.selector.register(self.process.stdout, selectors.EVENT_READ)
        except BaseException:
            self.failed = True
            self.close()
            raise

    def _exchange(self, operation, fields):
        if self.closed or self.failed or time.monotonic() >= self.deadline:
            raise ValueError('Polkit root observer has no live private custody')
        sequence = self.sequence
        self.sequence += 1
        envelope = {'format_version': 1, 'binding': self.execution, 'vm_id': self.vm_id,
                    'account': self.account, 'sequence': sequence, 'operation': operation}
        try:
            if self.process.poll() is not None or process_identity(self.process.pid) != self.process_birth:
                raise ValueError('Polkit observer SSH process changed')
            reply = Pipe(self.process, self.selector, self.deadline).line({**envelope, **fields})
            if (not isinstance(reply, dict) or set(reply) != {*envelope, 'result'}
                    or type(reply['format_version']) is not int or type(reply['sequence']) is not int
                    or any(reply[key] != value for key, value in envelope.items())
                    or time.monotonic() >= self.deadline
                    or process_identity(self.process.pid) != self.process_birth):
                raise ValueError('Polkit root observer private exchange lost its bound identity')
            return reply['result']
        except BaseException:
            self.failed = True
            self.close()
            raise

    def start(self, session, runtime):
        if self.started:
            raise ValueError('Polkit root observer cannot restart or replay its capture')
        self.started = True
        if self._exchange('start', {'session': session, 'runtime': runtime}) != {'monitoring': True}:
            self.failed = True
            self.close()
            raise ValueError('Polkit root observer did not establish its actual guest authorities')

    def bind(self, attempt):
        validate(attempt)
        if not self.started or self.bound:
            raise ValueError('Polkit root observer cannot rebind its selected transaction')
        self.bound = True
        return self._exchange('bind', {'attempt': attempt})

    def observe(self):
        if not self.bound:
            raise ValueError('Polkit root observer has no selected transaction')
        return self._exchange('observe', {})

    def close(self):
        if self.closed:
            return
        self.closed = True
        try:
            if self.process is not None:
                self.process.stdin.close()
                if self.process.poll() is None:
                    self.process.terminate()
                try:
                    self.process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.process.kill()
                    self.process.wait(timeout=5)
                self.process.stdout.close()
        finally:
            self.selector.close()

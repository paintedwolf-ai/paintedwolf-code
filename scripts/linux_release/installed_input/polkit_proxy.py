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

# This runs in isolated Python before importing any captured project code. The
# capture is prepared separately by the trusted controller, never by the QA UID.
BOOTSTRAP = '''import hashlib,json,stat,sys
from pathlib import Path
tree=sys.argv.pop(1)
root=Path('/var/lib/paintedwolf-qualification')/tree
for node in [*reversed(root.parents),root]:
 info=node.lstat()
 if not stat.S_ISDIR(info.st_mode) or info.st_uid!=0 or info.st_gid!=0 or info.st_mode&0o022:
  raise SystemExit(2)
marker=root/'capture.json'
info=marker.lstat()
if not stat.S_ISREG(info.st_mode) or info.st_uid!=0 or info.st_gid!=0 or info.st_mode&0o222 or info.st_size>1048576:
 raise SystemExit(2)
capture=json.loads(marker.read_bytes())
if set(capture)!={'format_version','driver_tree','files_sha256'} or type(capture['format_version']) is not int or capture['format_version']!=1 or capture['driver_tree']!=tree:
 raise SystemExit(2)
actual={}
for index,node in enumerate(root.rglob('*')):
 info=node.lstat()
 if index>=512 or info.st_uid!=0 or info.st_gid!=0 or info.st_mode&0o022 or not (stat.S_ISDIR(info.st_mode) or stat.S_ISREG(info.st_mode)):
  raise SystemExit(2)
 if stat.S_ISDIR(info.st_mode):
  if node.name=='__pycache__':
   raise SystemExit(2)
 elif node==marker:
  continue
 elif node.suffix=='.py' and not info.st_mode&0o222 and info.st_size<=1048576:
  actual[node.relative_to(root).as_posix()]=hashlib.sha256(node.read_bytes()).hexdigest()
 else:
  raise SystemExit(2)
if actual!=capture['files_sha256']:
 raise SystemExit(2)
sys.path.insert(0,str(root))
from scripts.linux_release.installed_input.polkit_root import main
main()
'''
BOOTSTRAP = 'try:\n' + ''.join(' ' + line for line in BOOTSTRAP.splitlines(keepends=True)) + \
    'except BaseException:\n raise SystemExit(2) from None\n'


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
                         'LANG=C.UTF-8', '/usr/bin/python3', '-I', '-B', '-c', BOOTSTRAP,
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

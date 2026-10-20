"""Compare owned host System V objects with the installed command's private IPC namespace."""
from __future__ import annotations

import ctypes
import secrets

from .kernel import run_script

SCRIPT = '''import ctypes, errno, json, pathlib, sys
libc = ctypes.CDLL(None, use_errno=True)
key = int(sys.argv[1])
operations = [('shared_memory', libc.shmget, (key, 4096, 0)),
              ('semaphore', libc.semget, (key, 1, 0)),
              ('message_queue', libc.msgget, (key, 0))]
result = {}
for name, operation, arguments in operations:
    ctypes.set_errno(0)
    found = operation(*arguments)
    result[name + '_hidden'] = found == -1 and ctypes.get_errno() == errno.ENOENT
created = []
try:
    for name, operation, arguments in operations:
        private_arguments = (0, *arguments[1:-1], 0o1600)
        found = operation(*private_arguments)
        result[name + '_private'] = found >= 0
        if found >= 0:
            created.append((name, found))
finally:
    for name, found in created:
        if name == 'shared_memory':
            libc.shmctl(found, 0, ctypes.c_void_p())
        elif name == 'semaphore':
            libc.semctl(found, 0, 0)
        else:
            libc.msgctl(found, 0, ctypes.c_void_p())
pathlib.Path('qualification-observation.json').write_text(json.dumps(result))
'''


class OwnedIPC:
    def __init__(self):
        self.libc = ctypes.CDLL(None, use_errno=True)
        self.key = secrets.randbelow((1 << 31) - 1) + 1
        self.created = {}

    def operations(self, flags):
        return [('shared_memory', self.libc.shmget, (self.key, 4096, flags)),
                ('semaphore', self.libc.semget, (self.key, 1, flags)),
                ('message_queue', self.libc.msgget, (self.key, flags))]

    def __enter__(self):
        try:
            for name, operation, arguments in self.operations(0o3600):
                value = operation(*arguments)
                if value < 0:
                    raise OSError(ctypes.get_errno(), 'owned System V fixture unavailable')
                self.created[name] = value
            if not self.intact():
                raise ValueError('host IPC fixture identity changed during setup')
            return self
        except BaseException:
            self.__exit__(None, None, None)
            raise

    def intact(self):
        return all(operation(*arguments) == self.created[name]
                   for name, operation, arguments in self.operations(0))

    def __exit__(self, kind, value, traceback):
        failed = False
        for name, value in self.created.items():
            if name == 'shared_memory':
                result = self.libc.shmctl(value, 0, ctypes.c_void_p())
            elif name == 'semaphore':
                result = self.libc.semctl(value, 0, 0)
            else:
                result = self.libc.msgctl(value, 0, ctypes.c_void_p())
            failed |= result != 0
        self.created.clear()
        if failed:
            raise OSError('owned IPC fixture cleanup failed')


def system_v_isolation(binding: dict) -> tuple[str, dict, dict]:
    with OwnedIPC() as host:
        invoked, facts, streams = run_script(binding, SCRIPT, [str(host.key)])
        observation = facts['kernel_observation']
        intact = host.intact()
        required = [name + suffix for name in host.created for suffix in ('_hidden', '_private')]
        passed = invoked and intact and all(observation.get(name) is True for name in required)
        facts['host_objects_preserved'] = intact
        facts['host_objects'] = dict(host.created)
    return 'passed' if passed else 'failed', facts, streams

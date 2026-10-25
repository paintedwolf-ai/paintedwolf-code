"""Keep detached Linux desktop launches beneath one real, bounded child owner."""
from __future__ import annotations

import ctypes
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time

LIMIT = 64 << 10


def process_identity(pid):
    fields = Path(f'/proc/{pid}/stat').read_text().rpartition(') ')[2].split()
    if len(fields) < 20:
        raise ValueError('installed process identity is unavailable')
    return {'pid': pid, 'parent_pid': int(fields[1]), 'start_time_ticks': int(fields[19])}


def children(pid):
    """Read only this owner's live descendant tree, including adopted children."""
    found, pending = [], [pid]
    while pending:
        parent = pending.pop()
        try:
            tasks = list(Path(f'/proc/{parent}/task').glob('*/children'))
            if len(tasks) > 512:
                raise ValueError('installed launch exceeded its thread bound')
            values = set()
            for task in tasks:
                try:
                    values.update(task.read_text().split())
                except FileNotFoundError:
                    continue
        except FileNotFoundError:
            continue
        for value in values:
            try:
                child = process_identity(int(value))
            except FileNotFoundError:
                continue
            if child['parent_pid'] != parent:
                continue
            found.append(child)
            if len(found) > 512:
                raise ValueError('installed launch exceeded its descendant bound')
            pending.append(child['pid'])
    return found


def signal_owned(row, sig):
    try:
        if process_identity(row['pid'])['start_time_ticks'] != row['start_time_ticks']:
            raise ValueError('installed descendant identity changed before settlement')
        os.kill(row['pid'], sig)
    except ProcessLookupError:
        pass
    except FileNotFoundError:
        pass


def reap(exclude):
    # Popen retains wait ownership for its direct command. Reap adopted children
    # individually so a racing command exit cannot lose its actual exit status.
    for row in children(os.getpid()):
        if row['parent_pid'] != os.getpid() or row['pid'] == exclude:
            continue
        try:
            os.waitpid(row['pid'], os.WNOHANG)
        except ChildProcessError:
            pass


def supervise(command, report, *, lifetime=300):
    """The child owns adoption and PID-pinned cleanup; it never changes cgroups."""
    libc = ctypes.CDLL(None, use_errno=True)
    if libc.prctl(36, 1, 0, 0, 0) != 0:  # PR_SET_CHILD_SUBREAPER
        raise OSError(ctypes.get_errno(), 'cannot own detached installed descendants')
    interrupted = False

    def stop(_sig, _frame):
        nonlocal interrupted
        interrupted = True

    for sig in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(sig, stop)
    parent, deadline = os.getppid(), time.monotonic() + lifetime
    launched = subprocess.Popen(command, stdin=subprocess.DEVNULL)
    command_done = False
    try:
        row = process_identity(launched.pid)
        report({'event': 'launched', 'command': command, 'identity': row,
                'owner': process_identity(os.getpid()), 'cgroup': Path(f'/proc/{launched.pid}/cgroup').read_text().strip()})
        while not interrupted and os.getppid() == parent and time.monotonic() < deadline:
            code = launched.poll()
            if code is not None and not command_done:
                report({'event': 'command-exit', 'identity': row, 'exit_code': code})
                command_done = True
                if code != 0:
                    return code
            reap(launched.pid)
            if command_done and not children(os.getpid()):
                return 0
            time.sleep(0.02)
        return 124 if not interrupted else 0
    finally:
        end = time.monotonic() + 3
        while True:
            descendants = children(os.getpid())
            if not descendants:
                reap(launched.pid)
                report({'event': 'settled', 'remaining': []})
                break
            sig = signal.SIGTERM if time.monotonic() < end else signal.SIGKILL
            for child in reversed(descendants):
                signal_owned(child, sig)
            launched.poll()
            reap(launched.pid)
            if time.monotonic() > end + 1:
                raise ValueError('installed launch descendants did not settle')
            time.sleep(0.02)


class LaunchOwner:
    """Parent-side evidence channel for a real Popen child, never a PID substitute."""
    def __init__(self, command, environment):
        read_fd, write_fd = os.pipe()
        self.events, self.failure, self.closed = [], None, threading.Event()
        try:
            self.process = subprocess.Popen(
                [sys.executable, str(Path(__file__).resolve()), str(write_fd), *command],
                env=environment, pass_fds=(write_fd,), stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
        except BaseException:
            os.close(read_fd)
            raise
        finally:
            os.close(write_fd)
        self.reader = threading.Thread(target=self._read, args=(read_fd,), daemon=True)
        self.reader.start()
        deadline = time.monotonic() + 10
        while not self.events and not self.closed.is_set() and time.monotonic() < deadline:
            self.closed.wait(0.02)
        if not self.events or self.events[0].get('event') != 'launched':
            self.process.terminate()
            self.process.wait(timeout=5)
            raise ValueError('installed detached launch did not establish its owned command')
        observed = self.events[0]
        if observed['owner'] != process_identity(self.process.pid) or observed['identity']['parent_pid'] != self.process.pid:
            self.process.terminate()
            self.process.wait(timeout=5)
            raise ValueError('installed detached launch owner identity differs')

    def _read(self, descriptor):
        try:
            with os.fdopen(descriptor, 'rb') as stream:
                total = 0
                while raw := stream.readline(LIMIT + 1):
                    total += len(raw)
                    if total > LIMIT or not raw.endswith(b'\n'):
                        raise ValueError('installed launch evidence exceeded its bound')
                    self.events.append(json.loads(raw))
        except BaseException as error:
            self.failure = str(error)
        finally:
            self.closed.set()

    def facts(self):
        if self.failure:
            raise ValueError(self.failure)
        return [dict(event) for event in self.events]


def main():
    descriptor = int(sys.argv[1])
    with os.fdopen(descriptor, 'w', buffering=1) as stream:
        def report(event):
            stream.write(json.dumps(event, sort_keys=True) + '\n')
        return supervise(sys.argv[2:], report)


if __name__ == '__main__':
    raise SystemExit(main())

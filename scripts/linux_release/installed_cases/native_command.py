"""Bound noninteractive native commands, their evidence and their owned child lifecycle."""
from __future__ import annotations

import math
import os
import selectors
import signal
import subprocess
import time

from .records import CaseUnavailable

LIMIT = 8 << 20
CLEANUP_SECONDS = 5


def remaining(deadline, reason):
    value = deadline - time.monotonic()
    if value <= 0:
        raise CaseUnavailable(reason)
    return value


def fallback_signal(pid, deadline):
    helper = subprocess.Popen(['/usr/bin/sudo', '-n', '/bin/kill', '-KILL', '--', '-' + str(pid)],
        stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    failures, cause = [], None
    # Reserve time for termination and kill/reap rather than spending the whole
    # shared cleanup budget waiting for a privileged helper to return.
    for action, divisor in ((None, 2), ('terminate', 2), ('kill', 1)):
        if helper.returncode is not None:
            break
        if action is not None:
            try:
                getattr(helper, action)()
            except BaseException as error:
                failures.append({'stage': action, 'failure_type': type(error).__name__})
                cause = error if cause is None else cause
        try:
            helper.wait(timeout=max(0, deadline - time.monotonic()) / divisor)
        except subprocess.TimeoutExpired:
            continue
        except BaseException as error:
            failures.append({'stage': 'wait', 'failure_type': type(error).__name__})
            cause = error if cause is None else cause
    return helper.returncode == 0, {'fallback_pid': helper.pid,
        'fallback_reaped': helper.returncode is not None, 'fallback_exit_code': helper.returncode,
        'fallback_signal_failures': failures}, cause


def settle(process):
    # wait() can reap a child before propagating KeyboardInterrupt. Never signal
    # a process-group number after its captured leader has already been reaped.
    if process.returncode is not None:
        return
    deadline = time.monotonic() + CLEANUP_SECONDS
    facts = {'pid': process.pid, 'group_signalled': False, 'reaped': False}
    cause = None
    try:
        try:
            os.killpg(process.pid, signal.SIGKILL)
            facts['group_signalled'] = True
        except ProcessLookupError:
            pass
        except PermissionError:
            facts['group_signalled'], fallback, cause = fallback_signal(process.pid, deadline)
            facts.update(fallback)
            if not facts['group_signalled']:
                raise CaseUnavailable('native-command-privileged-signal-unsettled')
    except BaseException as error:
        cause = error
    finally:
        try:
            process.wait(timeout=max(0, deadline - time.monotonic()))
            facts['reaped'] = True
        except BaseException as error:
            cause = error if cause is None else cause
    if cause is not None:
        error = CaseUnavailable('native-command-cleanup-unsettled')
        facts['failure_type'] = type(cause).__name__
        error.observations = {'native_command_cleanup': facts}
        raise error from cause


def command(arguments, *, timeout=300, limit=LIMIT, environment=None):
    if (type(timeout) not in (int, float) or not math.isfinite(timeout) or not 0 < timeout <= 300
            or type(limit) is not int or not 0 < limit <= LIMIT):
        raise ValueError('native command exceeds its fixed lifetime or evidence bounds')
    environment = ({'PATH': '/usr/sbin:/usr/bin:/sbin:/bin', 'LC_ALL': 'C', 'HOME': os.environ['HOME']}
                   if environment is None else dict(environment))
    process = subprocess.Popen(arguments, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, start_new_session=True, env=environment)
    buffers = {'stdout': bytearray(), 'stderr': bytearray()}
    deadline, completed = time.monotonic() + timeout, False
    try:
        with selectors.DefaultSelector() as selected:
            for stream, name in ((process.stdout, 'stdout'), (process.stderr, 'stderr')):
                os.set_blocking(stream.fileno(), False)
                selected.register(stream, selectors.EVENT_READ, name)
            while selected.get_map():
                for key, _ in selected.select(timeout=min(1, remaining(deadline, 'native-command-deadline'))):
                    output = buffers[key.data]
                    block = os.read(key.fileobj.fileno(), min(65536, limit + 1 - len(output)))
                    if not block:
                        selected.unregister(key.fileobj)
                        continue
                    if len(output) + len(block) > limit:
                        raise CaseUnavailable('native-command-evidence-bound')
                    output.extend(block)
        process.wait(timeout=remaining(deadline, 'native-command-deadline'))
        completed = True
        return process.returncode, bytes(buffers['stdout']), bytes(buffers['stderr'])
    except subprocess.TimeoutExpired:
        raise CaseUnavailable('native-command-deadline') from None
    finally:
        try:
            if not completed:
                settle(process)
        finally:
            process.stdout.close()
            process.stderr.close()

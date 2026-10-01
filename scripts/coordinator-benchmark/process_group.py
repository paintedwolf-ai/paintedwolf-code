"""Shutdown of the process group created for one owned launcher."""
import os
import signal
import subprocess
import time


def live_members(pgid):
    if pgid <= 1:
        raise ValueError('cleanup requires an explicit process group')
    rows = subprocess.check_output(['ps', '-axo', 'pgid=,stat='], text=True)
    live = False
    for row in rows.splitlines():
        group, state = row.split()
        if int(group) == pgid and state[0] != 'Z':
            live = True
    return live


def signal_group(pgid, sig):
    if pgid <= 1:
        raise ValueError('cleanup requires an explicit process group')
    try:
        os.killpg(pgid, sig)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        # Darwin may reject signals to a group containing only zombies.
        if live_members(pgid):
            raise
        return False


def stop_process(process, grace_seconds=30, kill_wait_seconds=30):
    if not signal_group(process.pid, signal.SIGTERM):
        return process.wait()
    deadline = time.monotonic() + grace_seconds
    while live_members(process.pid):
        process.poll()
        if time.monotonic() >= deadline:
            signal_group(process.pid, signal.SIGKILL)
            deadline = time.monotonic() + kill_wait_seconds
            while live_members(process.pid):
                process.poll()
                if time.monotonic() >= deadline:
                    raise TimeoutError('owned process group remains live after forced cleanup')
                time.sleep(0.05)
            break
        time.sleep(0.05)
    return process.wait()

#!/usr/bin/env python3
"""Terminate one target process group after a wall-clock deadline.

Go test binaries in the group first report where they are waiting: SIGUSR1 prints each pending
testutil wait, then SIGQUIT prints every goroutine stack.
"""

from __future__ import annotations

import os
import math
from pathlib import Path
import signal
import subprocess
import sys
import time

# Time for the pending waits to print before the stacks, and for the binaries to exit after them.
REPORT_SECONDS = 2
RELAY_SECONDS = 5


def timeout_seconds(value):
    timeout = float(value)
    if not math.isfinite(timeout) or timeout <= 0:
        raise ValueError("watchdog timeout must be a finite positive number of seconds")
    return timeout


def test_binaries(pgid):
    """Go test binaries in the group. Signalling the whole group would stop the go command relaying their output."""
    listing = subprocess.run(["ps", "-A", "-o", "pid=", "-o", "pgid=", "-o", "args="],
                             capture_output=True, text=True, check=False).stdout
    found = []
    for line in listing.splitlines():
        fields = line.split(None, 2)
        if len(fields) == 3 and fields[1] == str(pgid) and fields[2].split(" -test.", 1)[0].endswith(".test"):
            found.append(int(fields[0]))
    return found


def signal_each(pids, number):
    for pid in pids:
        try:
            os.kill(pid, number)
        except (ProcessLookupError, PermissionError):
            pass


def report_waits(pgid):
    binaries = test_binaries(pgid)
    if not binaries:
        return
    signal_each(binaries, signal.SIGUSR1)
    time.sleep(REPORT_SECONDS)
    signal_each(binaries, signal.SIGQUIT)
    relayed = time.monotonic() + RELAY_SECONDS
    while time.monotonic() < relayed and set(binaries) & set(test_binaries(pgid)):
        time.sleep(0.1)


def main() -> int:
    if len(sys.argv) == 3 and sys.argv[1] == "--validate":
        timeout_seconds(sys.argv[2])
        return 0
    if len(sys.argv) != 5:
        print("usage: process-group-watchdog.py SECONDS PGID FLAG DESCRIPTION", file=sys.stderr)
        return 2
    timeout = timeout_seconds(sys.argv[1])
    pgid = int(sys.argv[2])
    if pgid <= 1:
        raise ValueError("watchdog requires a process group id greater than one")
    flag = Path(sys.argv[3])
    description = sys.argv[4]
    deadline = time.monotonic() + timeout
    while True:
        try:
            os.killpg(pgid, 0)
        except ProcessLookupError:
            return 0
        except PermissionError:
            # A denied probe still means the process group exists.
            pass
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        time.sleep(min(remaining, 0.1))
    flag.touch()
    print(f"watchdog: exceeded {timeout:g}s; terminating: {description}", file=sys.stderr)
    report_waits(pgid)
    try:
        os.killpg(pgid, signal.SIGTERM)
    except ProcessLookupError:
        return 0
    time.sleep(5)
    try:
        os.killpg(pgid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except ValueError as error:
        print(f"watchdog: {error}", file=sys.stderr)
        raise SystemExit(2)

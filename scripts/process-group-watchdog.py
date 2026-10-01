#!/usr/bin/env python3
"""Terminate one target process group after a wall-clock deadline."""

from __future__ import annotations

import os
import math
from pathlib import Path
import signal
import sys
import time


def timeout_seconds(value):
    timeout = float(value)
    if not math.isfinite(timeout) or timeout <= 0:
        raise ValueError("watchdog timeout must be a finite positive number of seconds")
    return timeout


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

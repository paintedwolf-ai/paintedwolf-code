#!/usr/bin/env python3
"""Command line of the test-run lease: locks, path keys and collection for the digest shells."""

from __future__ import annotations

import fcntl
import hashlib
import os
from pathlib import Path
import sys
import time

from verification_lifecycle.run_lease import prune

HOLDERS_EXIT_SECONDS = 5


def await_holders_exit(candidate: Path) -> None:
    """Give a signaled test group time to exit: its dying processes hold the lease until they are gone."""
    try:
        descriptor = os.open(candidate / ".lease", os.O_RDWR | os.O_NOFOLLOW)
    except OSError:
        return
    with os.fdopen(descriptor, "r+") as lease:
        deadline = time.monotonic() + HOLDERS_EXIT_SECONDS
        while True:
            try:
                fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
                return
            except BlockingIOError:
                if time.monotonic() >= deadline:
                    return
                time.sleep(0.05)


def remove(root: Path, candidate: Path) -> None:
    await_holders_exit(candidate)
    prune(root, candidate)


if __name__ == "__main__":
    if sys.argv[1] == "lock":
        # flock follows the inherited open-file description held by the shell.
        fcntl.flock(int(sys.argv[2]), fcntl.LOCK_EX)
    elif sys.argv[1] == "try-lock":
        try:
            fcntl.flock(int(sys.argv[2]), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise SystemExit(1)
    elif sys.argv[1] == "path-key":
        print(hashlib.sha256(os.path.realpath(sys.argv[2]).encode()).hexdigest()[:12])
    elif sys.argv[1] == "prune":
        prune(Path(sys.argv[2]))
    elif sys.argv[1] == "remove":
        remove(Path(sys.argv[2]), Path(sys.argv[3]))
    else:
        raise SystemExit("expected lock, try-lock, path-key, prune, or remove")

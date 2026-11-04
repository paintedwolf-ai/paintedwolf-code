#!/usr/bin/env python3
"""Command line of the test-run lease: locks, path keys and collection for the digest shells."""

from __future__ import annotations

import fcntl
import hashlib
import os
from pathlib import Path
import sys

from verification_lifecycle.run_lease import prune

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
        prune(Path(sys.argv[2]), Path(sys.argv[3]))
    else:
        raise SystemExit("expected lock, try-lock, path-key, prune, or remove")

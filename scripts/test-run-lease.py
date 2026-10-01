#!/usr/bin/env python3
"""Kernel-held ownership for test-run directories shared by concurrent runners."""

from __future__ import annotations

import fcntl
import hashlib
import os
from pathlib import Path
import shutil
import stat
import sys


def remove_unleased(candidate: Path) -> None:
    if candidate.is_symlink() or not candidate.is_dir():
        return
    try:
        descriptor = os.open(candidate / ".lease", os.O_RDWR | os.O_NOFOLLOW)
    except OSError:
        return
    with os.fdopen(descriptor, "r+") as lease:
        try:
            fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return
        # Test fixtures can leave read-only directories behind.
        os.chmod(candidate, os.stat(candidate).st_mode | stat.S_IRWXU)
        for directory, children, _ in os.walk(candidate):
            for name in children:
                path = Path(directory) / name
                mode = path.lstat().st_mode
                if stat.S_ISDIR(mode):
                    os.chmod(path, mode | stat.S_IRWXU)
        shutil.rmtree(candidate)


def prune(root: Path, candidate: Path | None = None) -> None:
    if candidate is not None and candidate.absolute().parent != root.absolute():
        raise ValueError("test run must be an immediate child of its isolation root")
    if not root.is_dir():
        return
    with (root / ".collection.lock").open("a") as collection:
        fcntl.flock(collection, fcntl.LOCK_EX)
        for path in [candidate] if candidate is not None else root.iterdir():
            remove_unleased(path)


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

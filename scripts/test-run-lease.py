#!/usr/bin/env python3
"""Kernel-held ownership for test-run directories shared by concurrent runners."""

from __future__ import annotations

import fcntl
import hashlib
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys

UNMOUNT_TIMEOUT_SECONDS = 15


def mount_points() -> list[str]:
    """Current mount points, read without touching any of them: a dead one would hang a stat."""
    if sys.platform == "linux":
        with open("/proc/self/mountinfo", encoding="utf-8", errors="surrogateescape") as mounts:
            return [line.split()[4].encode().decode("unicode_escape") for line in mounts]
    table = subprocess.run(["/sbin/mount"], capture_output=True, text=True, timeout=UNMOUNT_TIMEOUT_SECONDS).stdout
    return [line.split(" on ", 1)[1].rsplit(" (", 1)[0] for line in table.splitlines() if " on " in line]


def force_unmount(mount_point: str) -> None:
    command = (
        ["fusermount3", "-u", "-z", "--", mount_point]
        if sys.platform == "linux"
        else ["/sbin/umount", "-f", mount_point]
    )
    try:
        subprocess.run(command, capture_output=True, timeout=UNMOUNT_TIMEOUT_SECONDS)
    except (OSError, subprocess.TimeoutExpired) as failure:
        print(f"test-run-lease: unmount {mount_point}: {failure}", file=sys.stderr)


def detach_mounts_within(candidate: Path) -> bool:
    """Detach mounts a killed test left in its run directory, and report whether none remain.

    The exclusive lease proves no run owns this directory. A test killed mid-run cannot unmount, and
    walking into such a mount would fail on a server that no longer answers, or delete through one
    that does.
    """
    prefix = os.path.realpath(candidate) + os.sep
    within = [point for point in mount_points() if point.startswith(prefix)]
    for point in sorted(within, key=len, reverse=True):
        force_unmount(point)
    return not within or not any(point.startswith(prefix) for point in mount_points())


def holds_a_mount(candidate: Path) -> bool:
    """A mount a test left behind answers from another device, or fails to answer at all."""
    device = candidate.lstat().st_dev
    for directory, children, _ in os.walk(candidate):
        for name in children:
            try:
                if (Path(directory) / name).lstat().st_dev != device:
                    return True
            except OSError:
                return True
    return False


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
        if not detach_mounts_within(candidate):
            print(f"test-run-lease: leaving {candidate}: a mount inside it could not be removed", file=sys.stderr)
            return
        # Removing through a stranded mount would reach its backing store or hang this runner.
        if holds_a_mount(candidate):
            print(f"test run keeps {candidate}: a mount remains inside it", file=sys.stderr)
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

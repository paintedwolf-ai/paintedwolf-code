#!/usr/bin/env python3
"""Kernel-held ownership for test-run directories shared by concurrent runners."""

from __future__ import annotations

import errno
import fcntl
import hashlib
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys

UNMOUNT_TIMEOUT_SECONDS = 15


def mount_points() -> list[str]:
    """Read the kernel's table, because a stat on a mount whose server died blocks for minutes."""
    if sys.platform == "linux":
        with open("/proc/self/mountinfo", "rb") as mounts:
            return [
                os.fsdecode(re.sub(rb"\\([0-7]{3})", lambda code: bytes([int(code[1], 8)]), line.split()[4]))
                for line in mounts
            ]
    listing = subprocess.run(
        ["/sbin/mount"], capture_output=True, check=True, timeout=UNMOUNT_TIMEOUT_SECONDS
    ).stdout
    return [
        os.fsdecode(line.split(b" on ", 1)[1].rsplit(b" (", 1)[0])
        for line in listing.splitlines()
        if b" on " in line
    ]


def force_unmount(mount_point: str) -> None:
    commands = (
        [["fusermount3", "-u", "-z", "--", mount_point], ["umount", "-l", mount_point]]
        if sys.platform == "linux"
        else [["/sbin/umount", "-f", mount_point], ["/usr/sbin/diskutil", "unmount", "force", mount_point]]
    )
    for command in commands:
        try:
            helper = subprocess.Popen(
                command, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL
            )
        except OSError:
            continue
        try:
            helper.wait(timeout=UNMOUNT_TIMEOUT_SECONDS)
        except subprocess.TimeoutExpired:
            # A helper stuck in the kernel may never exit, so it is killed but not awaited:
            # this runs under the collection lock that every starting run needs.
            helper.kill()
        if mount_point not in mount_points():
            return


def detach_mounts_within(candidate: Path) -> None:
    """Detach what a killed test left behind; the caller's exclusive lease proves no run owns it.

    Only this runner outlives a killed test, and walking into its mount would stall on a dead
    server or delete through a live one.
    """
    prefix = os.path.realpath(candidate) + os.sep
    within = [point for point in mount_points() if point.startswith(prefix)]
    for point in sorted(within, key=len, reverse=True):
        force_unmount(point)
    for point in mount_points():
        if point.startswith(prefix):
            raise OSError(errno.EBUSY, "mount could not be removed", point)


def remove_tree(candidate: Path) -> None:
    detach_mounts_within(candidate)
    device = os.lstat(candidate).st_dev
    # Test fixtures can leave read-only directories behind.
    os.chmod(candidate, os.stat(candidate).st_mode | stat.S_IRWXU)
    for directory, children, _ in os.walk(candidate):
        for name in children:
            path = Path(directory) / name
            status = os.lstat(path)
            if not stat.S_ISDIR(status.st_mode):
                continue
            # A mount that would not detach may have a dead server, or a live one whose
            # exported tree removal would delete through.
            if status.st_dev != device:
                raise OSError(errno.EBUSY, "mounted filesystem in test run", str(path))
            os.chmod(path, status.st_mode | stat.S_IRWXU)
    # The lease goes last: a partial removal is retried under its lease.
    for child in candidate.iterdir():
        if child.name == ".lease":
            continue
        if child.is_dir() and not child.is_symlink():
            shutil.rmtree(child)
        else:
            child.unlink()
    (candidate / ".lease").unlink(missing_ok=True)
    candidate.rmdir()


def remove_unleased(candidate: Path) -> None:
    if candidate.is_symlink() or not candidate.is_dir():
        return
    try:
        descriptor = os.open(candidate / ".lease", os.O_RDWR | os.O_NOFOLLOW)
    except FileNotFoundError:
        # Creation leases under the collection lock held here, so no run owns this
        # directory: its creation stopped partway.
        remove_tree(candidate)
        return
    except OSError:
        return
    with os.fdopen(descriptor, "r+") as lease:
        try:
            fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return
        remove_tree(candidate)


def prune(root: Path, candidate: Path | None = None) -> None:
    if candidate is not None and candidate.absolute().parent != root.absolute():
        raise ValueError("test run must be an immediate child of its isolation root")
    if not root.is_dir():
        return
    with (root / ".collection.lock").open("a") as collection:
        fcntl.flock(collection, fcntl.LOCK_EX)
        for path in [candidate] if candidate is not None else root.iterdir():
            # What cannot be removed stays for a later prune: leftovers must not fail a new run.
            try:
                remove_unleased(path)
            except (OSError, subprocess.SubprocessError) as failure:
                print(f"test-run-lease: keeping {path}: {failure}", file=sys.stderr)


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

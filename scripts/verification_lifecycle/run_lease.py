"""Kernel-held ownership for test-run directories shared by concurrent runners."""

from __future__ import annotations

import errno
import fcntl
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import time
from typing import NamedTuple

UNMOUNT_TIMEOUT_SECONDS = 15
OWNER_RELEASE_SECONDS = 15
HOLDERS_EXIT_SECONDS = 5
# Short paths keep Unix sockets within platform limits; test-run-isolation.sh names the same root.
RUN_ROOT = Path(f"/tmp/paintedwolf-test-runs-{os.getuid()}") if hasattr(os, "getuid") else None
SIGKILL_PENDING = 1 << 8
FUSE_CONNECTIONS = Path("/sys/fs/fuse/connections")


class Connection(NamedTuple):
    path: str
    device: str


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


def within(candidate: Path) -> str:
    return os.path.realpath(candidate) + os.sep


def detach_mounts_within(candidate: Path) -> None:
    """Detach what a killed test left behind; the caller's exclusive lease proves no run owns it.

    Only this runner outlives a killed test, and walking into its mount would stall on a dead
    server or delete through a live one.
    """
    prefix = within(candidate)
    present = [point for point in mount_points() if point.startswith(prefix)]
    for point in sorted(present, key=len, reverse=True):
        force_unmount(point)
    for point in mount_points():
        if point.startswith(prefix):
            raise OSError(errno.EBUSY, "mount could not be removed", point)


def fuse_connections(prefix: str) -> list[Connection]:
    """This user's FUSE connections mounted below prefix, from the kernel's table alone."""
    found = []
    with open("/proc/self/mountinfo", encoding="utf-8", errors="surrogateescape") as mounts:
        for line in mounts:
            left, _, right = line.partition(" - ")
            fields, details = left.split(), right.split()
            path = fields[4].encode().decode("unicode_escape")
            major, _, minor = fields[2].partition(":")
            if (path.startswith(prefix) and major == "0" and details[0].startswith("fuse")
                    and f"user_id={os.getuid()}" in details[2].split(",")):
                found.append(Connection(path, minor))
    return found


def abort_connection(connection: Connection) -> None:
    """End one connection. Its control file stays bound to it once open, so the recheck is exact."""
    descriptor = os.open(FUSE_CONNECTIONS / connection.device / "abort", os.O_WRONLY)
    try:
        if connection in fuse_connections(connection.path):
            os.write(descriptor, b"1")
    finally:
        os.close(descriptor)


def thread_group_dying(pid: int) -> bool:
    """Every thread is gone or has SIGKILL pending, so none can run user code again."""
    tasks = Path(f"/proc/{pid}/task")
    try:
        threads = list(tasks.iterdir())
        for thread in threads:
            state = (thread / "stat").read_text(errors="replace").rsplit(")", 1)[1].split()[0]
            if state in {"Z", "X"}:
                continue
            for line in (thread / "status").read_text(errors="replace").splitlines():
                if line.startswith("SigPnd:"):
                    if not int(line.split()[1], 16) & SIGKILL_PENDING:
                        return False
                    break
            else:
                return False
    except FileNotFoundError:
        # A thread that exited between reads has no claim on the mount.
        return True
    except (OSError, IndexError, ValueError):
        return False
    return bool(threads)


def release_stranded_connections(candidate: Path, killed: frozenset = frozenset()) -> bool:
    """End the mounts inside a run all of whose lease holders are proven dead.

    A process killed while it waits on its own FUSE server holds that server's device open, so the
    connection never ends, its waiter never exits, and the run's lease is never released; a detach
    leaves all of that in place. On macOS a kill cannot be observed from outside, so a dead holder is
    one this process signalled, by its birth identity, and a forced unmount wakes it.
    """
    linux = sys.platform == "linux"
    if not linux and not killed:
        return False
    prefix = within(candidate)
    connections = fuse_connections(prefix) if linux else [
        Connection(point, "") for point in mount_points() if point.startswith(prefix)]
    if not connections:
        return False
    # The ordinary lease paths run without the scheduler's modules, so only this one loads them.
    from verification_lifecycle import custody

    holders = custody.lease_holders(candidate / ".lease") - {os.getpid()}
    if not holders or not all(thread_group_dying(pid) if linux else (pid, custody.birth(pid)) in killed
                              for pid in holders):
        return False
    for connection in sorted(connections, key=lambda item: len(item.path), reverse=True):
        try:
            if linux:
                abort_connection(connection)
            else:
                force_unmount(connection.path)
        except OSError as failure:
            print(f"test-run-lease: abort {connection.path}: {failure}", file=sys.stderr)
    return True


def acquire_lease(candidate: Path, lease, killed: frozenset) -> bool:
    try:
        fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
        return True
    except BlockingIOError:
        pass
    try:
        if not release_stranded_connections(candidate, killed):
            return False
    except (OSError, ValueError, subprocess.SubprocessError) as failure:
        print(f"test-run-lease: inspecting {candidate}: {failure}", file=sys.stderr)
        return False
    deadline = time.monotonic() + OWNER_RELEASE_SECONDS
    while True:
        try:
            fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return True
        except BlockingIOError:
            if time.monotonic() >= deadline:
                return False
            time.sleep(0.05)


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


def remove_unleased(candidate: Path, killed: frozenset = frozenset()) -> None:
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
        if acquire_lease(candidate, lease, killed):
            remove_tree(candidate)


def prune(root: Path, candidate: Path | None = None, killed: frozenset = frozenset()) -> None:
    if candidate is not None and candidate.absolute().parent != root.absolute():
        raise ValueError("test run must be an immediate child of its isolation root")
    if not root.is_dir():
        return
    with (root / ".collection.lock").open("a") as collection:
        fcntl.flock(collection, fcntl.LOCK_EX)
        for path in [candidate] if candidate is not None else root.iterdir():
            # What cannot be removed stays for a later prune: leftovers must not fail a new run.
            try:
                remove_unleased(path, killed)
            except (OSError, subprocess.SubprocessError) as failure:
                print(f"test-run-lease: keeping {path}: {failure}", file=sys.stderr)


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


def collect_runs(killed: frozenset = frozenset()) -> None:
    """Teardown's collection: settles what a killed run stranded, and never fails its caller."""
    if RUN_ROOT is None:
        return
    try:
        prune(RUN_ROOT, killed=killed)
    except (OSError, ValueError, subprocess.SubprocessError) as failure:
        print(f"test-run-lease: collecting runs: {failure}", file=sys.stderr)

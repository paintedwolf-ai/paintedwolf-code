"""Kernel-held ownership for test-run directories shared by concurrent runners."""

from __future__ import annotations

import fcntl
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import time
from typing import NamedTuple

UNMOUNT_TIMEOUT_SECONDS = 15
OWNER_RELEASE_SECONDS = 15
# Short paths keep Unix sockets within platform limits; test-run-isolation.sh names the same root.
RUN_ROOT = Path(f"/tmp/paintedwolf-test-runs-{os.getuid()}") if hasattr(os, "getuid") else None
SIGKILL_PENDING = 1 << 8
FUSE_CONNECTIONS = Path("/sys/fs/fuse/connections")


class Connection(NamedTuple):
    path: str
    device: str


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


def within(candidate: Path) -> str:
    return os.path.realpath(candidate) + os.sep


def detach_mounts_within(candidate: Path) -> bool:
    """Detach mounts a killed test left in its run directory, and report whether none remain.

    The exclusive lease proves no run owns this directory. A test killed mid-run cannot unmount, and
    walking into such a mount would fail on a server that no longer answers, or delete through one
    that does.
    """
    prefix = within(candidate)
    present = [point for point in mount_points() if point.startswith(prefix)]
    for point in sorted(present, key=len, reverse=True):
        force_unmount(point)
    return not present or not any(point.startswith(prefix) for point in mount_points())


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


def remove_unleased(candidate: Path, killed: frozenset = frozenset()) -> None:
    if candidate.is_symlink() or not candidate.is_dir():
        return
    try:
        descriptor = os.open(candidate / ".lease", os.O_RDWR | os.O_NOFOLLOW)
    except OSError:
        return
    with os.fdopen(descriptor, "r+") as lease:
        if not acquire_lease(candidate, lease, killed):
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


def prune(root: Path, candidate: Path | None = None, killed: frozenset = frozenset()) -> None:
    if candidate is not None and candidate.absolute().parent != root.absolute():
        raise ValueError("test run must be an immediate child of its isolation root")
    if not root.is_dir():
        return
    with (root / ".collection.lock").open("a") as collection:
        fcntl.flock(collection, fcntl.LOCK_EX)
        for path in [candidate] if candidate is not None else root.iterdir():
            remove_unleased(path, killed)


def collect_runs(killed: frozenset = frozenset()) -> None:
    """Teardown's collection: settles what a killed run stranded, and never fails its caller."""
    if RUN_ROOT is None:
        return
    try:
        prune(RUN_ROOT, killed=killed)
    except (OSError, ValueError, subprocess.SubprocessError) as failure:
        print(f"test-run-lease: collecting runs: {failure}", file=sys.stderr)

"""Kernel capability and process-creation identity for owned verification children."""

import os
from pathlib import Path
import shutil
import signal
import subprocess

from verification_health import identity


def lease_holders(path):
    """Find holders by device/inode, including descendants that changed sessions."""
    target = Path(path).stat()
    if Path("/proc/self/fd").is_dir():
        found = set()
        for descriptor in Path("/proc").glob("[0-9]*/fd/*"):
            try:
                opened = descriptor.stat()
            except OSError:
                continue
            if (opened.st_dev, opened.st_ino) == (target.st_dev, target.st_ino):
                found.add(int(descriptor.parts[2]))
        return found
    result = subprocess.run([shutil.which("lsof") or "/usr/sbin/lsof", "-t", "-w", "--", str(path)],
                            capture_output=True, text=True, timeout=30)
    if result.returncode not in {0, 1}:
        raise OSError("Lease holder inspection failed")
    return {int(pid) for pid in result.stdout.split()}


def commands(pids):
    if not pids:
        return {}
    result = subprocess.run(["ps", "-o", "pid=,comm=", "-p", ",".join(map(str, sorted(pids)))],
                            capture_output=True, text=True, timeout=5)
    if result.returncode not in {0, 1}:
        raise OSError("Process identity inspection failed")
    named = {}
    for line in result.stdout.splitlines():
        pid, _, command = line.strip().partition(" ")
        named[int(pid)] = Path(command.strip()).name
    return named


def birth(pid):
    if Path("/proc/self/stat").is_file():
        try:
            # comm can contain spaces and parentheses; fields after its final ')' are numeric.
            return Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()[19]
        except FileNotFoundError:
            return None
    return identity(pid)


def holds(pid, path, expected):
    """Recheck this process only; a global holder scan is not needed per signal."""
    if Path("/proc/self/fd").is_dir():
        try:
            descriptors = list(Path(f"/proc/{pid}/fd").iterdir())
        except FileNotFoundError:
            return False
        for descriptor in descriptors:
            try:
                value = descriptor.stat()
            except FileNotFoundError:
                continue
            if (value.st_dev, value.st_ino) == expected:
                return True
        return False
    value = Path(path).stat()
    if (value.st_dev, value.st_ino) != expected:
        raise OSError("Lease identity changed")
    result = subprocess.run([shutil.which("lsof") or "/usr/sbin/lsof", "-a", "-p", str(pid),
                             "-t", "-w", "--", str(path)], capture_output=True, text=True, timeout=30)
    if result.returncode not in {0, 1}:
        raise OSError("Process capability inspection failed")
    return str(pid) in result.stdout.split()


def signal_holder(pid, created, path, expected, signum):
    """A birth identity and the exact retained capability must still match."""
    descriptor = None
    try:
        if hasattr(os, "pidfd_open"):
            descriptor = os.pidfd_open(pid)
        if created is None or birth(pid) != created or not holds(pid, path, expected):
            return False
        if descriptor is not None:
            signal.pidfd_send_signal(descriptor, signum)
        else:
            # Platforms without pidfds still revalidate birth after inspecting the capability.
            if birth(pid) != created:
                return False
            os.kill(pid, signum)
        return True
    except ProcessLookupError:
        return False
    finally:
        if descriptor is not None:
            os.close(descriptor)

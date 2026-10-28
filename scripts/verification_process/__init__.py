"""Observe verification process identities and activity through native facts."""
from functools import lru_cache
import importlib
import os
import subprocess
import sys


def process_snapshot(pid=None):
    if os.name == "nt":
        raise OSError("Process activity sampling is unavailable on this platform")
    selection = ["-p", str(pid)] if pid is not None else ["-ax"]
    result = subprocess.run(["ps", *selection, "-o", "pid=,ppid=,lstart=,stat=,time="],
                            capture_output=True, text=True, timeout=5, env={**os.environ, "LC_ALL": "C"})
    if result.returncode != 0:
        raise OSError("Process activity sampling failed")
    records = {}
    for line in result.stdout.splitlines():
        fields = line.split()
        if len(fields) != 9:
            raise ValueError("Incomplete process activity sample")
        number, parent = map(int, fields[:2])
        records[number] = {"pid": number, "parent": parent, "identity": " ".join(fields[2:7]),
                           "state": fields[7], "cpu": fields[8]}
    return records


def identity(pid):
    try:
        return process_snapshot(pid)[pid]["identity"]
    except (OSError, ValueError, KeyError, subprocess.TimeoutExpired):
        return None


@lru_cache(maxsize=8)
def owner_identity(pid):
    return identity(pid)


def ownership(pid, expected, processes):
    record = processes.get(pid)
    if record is None or record["state"].startswith("Z"):
        return "exited"
    if expected is None:
        return "unknown"
    return "present" if record["identity"] == expected else "replaced"


def descendants(pid, processes):
    found = {pid} if pid in processes else set()
    children = {}
    for number, record in processes.items():
        children.setdefault(record["parent"], []).append(number)
    pending = list(found)
    while pending:
        for child in children.get(pending.pop(), []):
            if child not in found:
                found.add(child)
                pending.append(child)
    return [processes[number] for number in sorted(found)]


class ProcessObserver:
    """Pair a native birth with the creation identity recorded by the queue."""

    def birth(self, pid):
        facts = self.native_birth(pid)
        facts['owner_identity'] = identity(pid)
        return facts


NATIVE_MODULES = {"linux": "verification_process.linux", "darwin": "verification_process.darwin"}


def dependencies():
    try:
        name = NATIVE_MODULES[sys.platform]
    except KeyError as error:
        raise OSError("Verification process descriptors are unavailable on this platform") from error
    return {name: "scripts/" + name.replace(".", "/") + ".py"}


def observer():
    return importlib.import_module(next(iter(dependencies()))).Observer()

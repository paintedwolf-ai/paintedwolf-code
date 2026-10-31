"""Advisory process health from ownership and activity samples."""

from functools import lru_cache
import ctypes
import ctypes.util
import os
from pathlib import Path
import struct
import subprocess
import sys
import time

import verification_advisories as advisories
from verification_state import read_state, write_json

SAMPLE_SECONDS = 30
QUIET_SECONDS = 600
MAX_SAMPLE_GAP = 90
NOTICE_STATES = {"supervision_lost", "supervisor_stopped", "suspected_stall", "cleanup_blocked"}
_DARWIN_KINFO_PROC_SIZE = 648
_DARWIN_PROC_TASKINFO_SIZE = 96
_DARWIN_STATES = {1: "I", 2: "R", 3: "S", 4: "T", 5: "Z"}


@lru_cache(maxsize=1)
def _darwin_libc():
    path = ctypes.util.find_library("c")
    return ctypes.CDLL(path, use_errno=True) if path else None


def _darwin_cpu_time(libc, pid):
    buf = ctypes.create_string_buffer(_DARWIN_PROC_TASKINFO_SIZE)
    if libc.proc_pidinfo(int(pid), 4, 0, buf, _DARWIN_PROC_TASKINFO_SIZE) != _DARWIN_PROC_TASKINFO_SIZE:
        return "0:00.00"
    user_ns, sys_ns = struct.unpack_from("<QQ", buf.raw, 16)
    total_seconds = (user_ns + sys_ns) / 1_000_000_000
    minutes, seconds = divmod(total_seconds, 60)
    return f"{int(minutes)}:{seconds:05.2f}"


def _darwin_process_snapshot(pid=None):
    libc = _darwin_libc()
    if libc is None:
        raise OSError("Process activity sampling is unavailable on this platform")
    if pid is not None:
        mib = (ctypes.c_int * 4)(1, 14, 1, int(pid))
        mib_len = 4
    else:
        mib = (ctypes.c_int * 3)(1, 14, 0)
        mib_len = 3
    size = ctypes.c_size_t(0)
    if libc.sysctl(mib, mib_len, None, ctypes.byref(size), None, 0) != 0 or size.value == 0:
        raise OSError("Process activity sampling failed")
    extra = 0 if pid is not None else _DARWIN_KINFO_PROC_SIZE * 32
    buf = ctypes.create_string_buffer(size.value + extra)
    size.value = len(buf)
    if libc.sysctl(mib, mib_len, buf, ctypes.byref(size), None, 0) != 0:
        raise OSError("Process activity sampling failed")
    raw = buf.raw[:size.value]
    count = len(raw) // _DARWIN_KINFO_PROC_SIZE
    records = {}
    for index in range(count):
        offset = index * _DARWIN_KINFO_PROC_SIZE
        tv_sec = struct.unpack_from("<q", raw, offset)[0]
        stat_code = struct.unpack_from("<b", raw, offset + 36)[0]
        number = struct.unpack_from("<i", raw, offset + 40)[0]
        if number <= 0:
            continue
        parent = struct.unpack_from("<i", raw, offset + 560)[0]
        comm = raw[offset + 243:offset + 260].split(b"\x00", 1)[0].decode("utf-8", errors="replace")
        # Format start time with ctime so sysctl records match LC_ALL=C ps lstart output.
        started = " ".join(time.ctime(tv_sec).split())
        records[number] = {
            "pid": number,
            "parent": parent,
            "identity": started,
            "state": _DARWIN_STATES.get(stat_code, "S"),
            "cpu": _darwin_cpu_time(libc, number),
            "command": comm,
        }
    if pid is not None and int(pid) not in records:
        raise OSError("Process activity sampling failed")
    return records


def process_snapshot(pid=None):
    if os.name == "nt":
        raise OSError("Process activity sampling is unavailable on this platform")
    selection = ["-p", str(pid)] if pid is not None else ["-ax"]
    try:
        result = subprocess.run(["ps", *selection, "-o", "pid=,ppid=,lstart=,stat=,time="],
                                capture_output=True, text=True, timeout=5, env={**os.environ, "LC_ALL": "C"})
    except (OSError, subprocess.SubprocessError):
        if sys.platform == "darwin":
            return _darwin_process_snapshot(pid)
        raise
    if result.returncode != 0:
        if sys.platform == "darwin":
            return _darwin_process_snapshot(pid)
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


def assess(entry, processes, previous, now):
    owner = ownership(entry.get("pid"), entry.get("owner_identity"), processes)
    health = {"state": "observing", "owner": owner, "action": "none"}
    if owner in {"exited", "replaced"}:
        health.update(state="supervision_lost",
                      reason="The recorded owner is no longer running; ./task test:cancel stops the processes still holding its lease")
        return health, {}
    if owner == "unknown":
        health.update(state="unknown", reason="Owner creation identity was not recorded")
        return health, {}
    if processes[entry["pid"]]["state"].startswith("T"):
        health.update(state="supervisor_stopped", reason="The operating system reports the owner as stopped")
        return health, {}
    if entry.get("terminal_settlement", {}).get("state") == "blocked":
        health.update(state="cleanup_blocked", reason="Terminal producer cleanup is unverified; admission remains held")
        return health, {}
    if entry.get("kind") == "batch" and entry.get("phase") == "execution":
        health.update(state="supervised", reason="Activity is assessed separately for each operation")
        return health, {}
    child = ownership(entry.get("child_pid"), entry.get("child_identity"), processes)
    health["child"] = child
    if child != "present":
        health.update(state="unknown", reason="No matching live command identity in this sample")
        return health, {}
    tree = descendants(entry["child_pid"], processes)
    fingerprint = [[p["pid"], p["identity"], p["cpu"]] for p in tree]
    outputs = []
    try:
        for name in entry.get("activity_paths", []):
            try:
                stat = Path(name).stat()
                outputs.append([name, stat.st_ino, stat.st_size, stat.st_mtime_ns])
            except FileNotFoundError:
                outputs.append([name, None])
    except OSError:
        health.update(state="unknown", reason="Output activity could not be sampled")
        return health, {}
    fingerprint.append(outputs)
    gap = now - previous.get("sampled_at", now)
    continuous = bool(previous) and 0 <= gap <= MAX_SAMPLE_GAP
    changed = not continuous or fingerprint != previous.get("fingerprint")
    since = now if changed else previous["activity_at"]
    samples = 1 if changed else previous["samples"] + (gap >= 1)
    quiet = max(0, now - since)
    health.update(state="active" if continuous and changed else "observing",
                  quiet_seconds=round(quiet, 1), samples=samples,
                  process_count=len(tree), processes=tree, output_paths=entry.get("activity_paths", []))
    if quiet >= QUIET_SECONDS and samples >= 3:
        health.update(state="suspected_stall",
                      reason="No observed descendant CPU, process-tree, or output change; a legitimate wait is still possible")
    return health, {"fingerprint": fingerprint, "sampled_at": now, "activity_at": since, "samples": samples}


def annotate(queue, status):
    monitored = [e for e in status["runs"] if e["state"] == "running"]
    # An invocation's own reservation shares its owner and child; the invocation already reports them.
    invocations = {e["ticket"] for e in monitored if e.get("kind") == "invocation"}
    monitored.extend(e for e in status["operations"] if e["state"] == "running" and "pid" in e
                     and e.get("batch") not in invocations)
    if not monitored:
        return status
    try:
        processes = process_snapshot()
        # Observers share the system clock.
        now = time.clock_gettime(time.CLOCK_MONOTONIC)
    except (OSError, ValueError, subprocess.TimeoutExpired) as error:
        for entry in monitored:
            entry["health"] = {"state": "unknown", "reason": str(error), "action": "none"}
        return status
    with queue.locked():
        path = queue.root / "health.json"
        previous = read_state(path)
        observed = {}
        for entry in monitored:
            key = entry["ticket"]
            entry["health"], observed[key] = assess(entry, processes, previous.get(key, {}), now)
            entry["health"]["observed_at"] = time.time()
        write_json(path, observed)
    return status


def health_notices(status):
    for entry in [*status["runs"], *status["operations"]]:
        health = entry.get("health", {})
        if health.get("state") in NOTICE_STATES:
            yield (entry["ticket"], health["state"],
                   f"test execution: {entry['name']} ({entry['ticket']}): {health['state']}: "
                   f"{health['reason']}. {advisories.TRAILER}")


def report(queue, ticket=None):
    """`ticket` identifies the observer's own request, which sees advisories about its own batch."""
    try:
        status = advisories.observe(queue, annotate(queue, queue.status()))
    except (OSError, ValueError) as error:
        print(f"test execution: health diagnostics unavailable: {error}", file=sys.stderr, flush=True)
        return
    for subject, code, text in [*health_notices(status), *advisories.notices(status, ticket)]:
        advisories.announce(subject, code, text)

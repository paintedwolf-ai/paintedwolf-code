"""Withdraw one queued or running verification request, with a recorded reason."""

import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import time

from verification_batch import status_details
from verification_health import ownership, process_snapshot
from verification_state import read_state, write_json

USAGE = 'usage: ./task test:cancel -- <ticket> --reason "<why>" [--force]'
ADVERSE_HEALTH = {"supervision_lost", "supervisor_stopped", "suspected_stall"}
MAX_RECORDS = 64
RELEASE_SECONDS = 15
RECLAIM_SECONDS = 5


def parse(arguments):
    ticket, reason, force = None, None, False
    index = 0
    while index < len(arguments):
        name, separator, value = arguments[index].partition("=")
        if name == "--force":
            force = True
        elif name == "--reason":
            if not separator:
                index += 1
                value = arguments[index] if index < len(arguments) else ""
            if not value or value.startswith("-"):
                raise ValueError(f"--reason requires text. {USAGE}")
            reason = value
        elif name.startswith("-"):
            raise ValueError(f"unsupported option {arguments[index]!r}. {USAGE}")
        elif ticket is not None:
            raise ValueError(f"cancel one request at a time. {USAGE}")
        else:
            ticket = arguments[index]
        index += 1
    if not ticket or not reason:
        raise ValueError(f"a ticket and a reason are required. {USAGE}")
    return ticket, reason, force


def select(entries, prefix):
    """Tickets are long; a prefix addresses one request as long as it stays unambiguous."""
    matches = [entry for entry in entries if entry["ticket"].startswith(prefix)]
    exact = [entry for entry in matches if entry["ticket"] == prefix]
    requests = [entry for entry in matches if entry.get("kind") == "request"]
    for candidates in (exact, requests, matches):
        if len(candidates) == 1:
            return candidates[0]
    if not matches:
        raise ValueError(f"no queued or running request matches ticket {prefix!r}; inspect ./task test:status")
    raise ValueError(f"ticket {prefix!r} matches {len(matches)} entries: "
                     + ", ".join(f"{entry['name']} ({entry['ticket']})" for entry in matches))


def batch_of(status, entry):
    assignment = entry.get("batch")
    if not assignment:
        return None
    return next((run for run in status["runs"] if run["ticket"] == assignment["ticket"]), None)


def shared_with_others(status, entry):
    batch = batch_of(status, entry)
    if batch is None:
        return False
    live = {run["ticket"] for run in status["runs"]}
    return bool(set(batch.get("members", [])).intersection(live) - {entry["ticket"]})


def refusal(status, entry, force):
    if entry.get("kind") == "batch":
        live = {run["ticket"] for run in status["runs"]}
        members = [member for member in entry.get("members", []) if member in live]
        if not members:
            # Every subscriber is gone, so only leftover holders keep this lease.
            return None
        return (f"{entry['ticket']} is the shared batch supervisor, not a request. Cancel the request that no "
                f"longer needs its result: {', '.join(members)}")
    if force or entry["state"] == "queued":
        return None
    if shared_with_others(status, entry):
        return None
    health = (entry.get("health") or {}).get("state")
    if health in ADVERSE_HEALTH:
        return None
    observed = f"health is {health}" if health else "it has no adverse health observation"
    return (f"{entry['name']} ({entry['ticket']}) is verifying work no other request shares and {observed}. "
            f"Cancelling discards that work; pass --force with a reason if that is what you intend")


def record(queue, status, entry, reason, force):
    value = {"ticket": entry["ticket"], "name": entry["name"], "state": entry["state"],
             "source": entry.get("source"), "reason": reason, "forced": force,
             "cancelled_at": time.time(), "cancelled_by": os.getpid()}
    batch = batch_of(status, entry)
    if batch is not None and batch.get("directory"):
        # The executor attaches this reason to the member's cancelled receipt.
        write_json(Path(batch["directory"]) / (entry["ticket"] + ".cancelled.json"), value)
    with queue.locked():
        path = queue.root / "cancellations.json"
        log = read_state(path).get("cancellations", [])
        log = [item for item in log if isinstance(item, dict)][-(MAX_RECORDS - 1):]
        write_json(path, {"cancellations": [*log, value]})
    return value


def released(queue, ticket):
    with queue.locked():
        return all(entry["ticket"] != ticket for entry in queue.entries())


def await_release(queue, ticket, seconds):
    deadline = time.monotonic() + seconds
    while not released(queue, ticket) and time.monotonic() < deadline:
        time.sleep(0.1)
    return released(queue, ticket)


def lease_holders(path):
    """Processes with the lease file open, by inode so a renamed path still matches."""
    target = path.stat()
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
    lsof = shutil.which("lsof") or "/usr/sbin/lsof"
    result = subprocess.run([lsof, "-t", "-w", "--", str(path)], capture_output=True, text=True, timeout=30)
    # lsof exits 1 when nothing has the file open.
    if result.returncode not in {0, 1}:
        raise OSError(f"lsof could not list holders of {path}: {result.stderr.strip()}")
    return {int(pid) for pid in result.stdout.split()}


def commands(pids):
    if not pids:
        return {}
    result = subprocess.run(["ps", "-o", "pid=,comm=", "-p", ",".join(map(str, sorted(pids)))],
                            capture_output=True, text=True, timeout=5)
    named = {}
    for line in result.stdout.splitlines():
        pid, _, command = line.strip().partition(" ")
        named[int(pid)] = command.strip()
    return named


def reclaim(queue, ticket):
    """Stop every process holding the request's lease so a withdrawal always releases admission.

    Only the run's own descendants inherit its lease descriptor, and status readers open leases
    only under the queue lock, so every holder listed under that lock belongs to the run, including
    processes that left its process group and survived its supervisor.
    """
    path = queue.root / (ticket + ".lease")
    stopped = {}
    for signum in (signal.SIGTERM, signal.SIGKILL):
        with queue.locked():
            try:
                holders = lease_holders(path) - {os.getpid()}
            except FileNotFoundError:
                break
            names = commands(holders)
            for pid in sorted(holders):
                try:
                    os.kill(pid, signum)
                except ProcessLookupError:
                    continue
                stopped[pid] = {"pid": pid, "command": names.get(pid, stopped.get(pid, {}).get("command")),
                                "signal": signal.Signals(signum).name}
        if await_release(queue, ticket, RECLAIM_SECONDS):
            break
    return [stopped[pid] for pid in sorted(stopped)]


def cancel(queue, arguments):
    prefix, reason, force = parse(arguments)
    status = status_details(queue)
    entry = select(status["runs"], prefix)
    refused = refusal(status, entry, force)
    if refused:
        raise ValueError(refused)
    if os.name == "nt":
        raise ValueError("cancellation needs the POSIX process sampler to prove the owner before signalling it; "
                         "interrupt the command that holds the request instead")
    value = record(queue, status, entry, reason, force)
    # Creation identity keeps a recycled pid from being signalled in the owner's place.
    owner = ownership(entry.get("pid"), entry.get("owner_identity"), process_snapshot())
    if owner == "present":
        try:
            os.kill(entry["pid"], signal.SIGTERM)
        except (OSError, TypeError):
            owner = "exited"
    value["owner"] = owner
    # A present owner gets the ordinary path first so it can write its own cancelled receipt.
    value["released"] = await_release(queue, entry["ticket"], RELEASE_SECONDS if owner == "present" else 0)
    if not value["released"]:
        value["reclaimed"] = reclaim(queue, entry["ticket"])
        value["released"] = released(queue, entry["ticket"])
    print(json.dumps(value, indent=2, sort_keys=True))
    return 0 if value["released"] else 2

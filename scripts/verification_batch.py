"""Bounded batches, independent subscribers, and retained verification receipts."""

import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import time
import uuid

import verification_advisories as advisories
from verification_history import estimate, read_history
from verification_plan import compatibility, outcome_exit
from verification_resources import blocked_reason, foreground_git_environment, order_of
from verification_health import annotate, identity, owner_identity, report
from artifact_paths import artifact_root
from verification_state import read_json, read_optional, write_json


MAX_REQUESTS = 32
MAX_ACTIVE_BATCHES = 8
KEEP_BATCHES = 20
MAX_RETAINED_BYTES = 512 * 1024 * 1024
LEASE_FD = 200


def write_lease(lease, entry):
    body = json.dumps(entry)
    body = body.ljust(max(len(body), os.fstat(lease.fileno()).st_size))
    lease.seek(0)
    lease.write(body)
    lease.flush()


def unfinished_receipt(directory, request, reason):
    source = read_optional(directory / "source.json") or {}
    resolved = read_optional(directory / "resolved-plan.json") or []
    request = next((member for member in resolved if member["ticket"] == request["ticket"]), request)
    progress = read_optional(directory / (request["ticket"] + ".progress.json")) or {}
    completed = set(progress.get("completed", []))
    return {"request": request["ticket"], "names": request["plan"]["names"], "batch_id": directory.name,
            "source_root": request["source"], **source, "status": "unverified", "exit_code": 2,
            "reason": reason, "finished_at": time.time(), "evidence": progress.get("evidence", []),
            "unverified_stages": [stage["name"] for index, stage in enumerate(request["plan"]["stages"])
                                  if index not in completed]}


def recover_abandoned(entry):
    if entry.get("kind") != "batch":
        return
    directory = Path(entry["directory"])
    plan = read_optional(directory / "plan.json")
    if plan is None or (directory / "finished.json").exists():
        return
    for request in plan["requests"]:
        receipt = directory / (request["ticket"] + ".json")
        if not receipt.exists():
            write_json(receipt, unfinished_receipt(directory, request, "All batch execution processes exited"))
    write_json(directory / "finished.json", {"finished_at": time.time()})


def active_members(queue, members):
    with queue.locked():
        live = {entry["ticket"] for entry in queue.entries()}
    return live.intersection(members)


def admissions(entry):
    return entry.get("admissions") or [entry["admission"]]


def waiting_admissions(entry):
    return [{**spec, "ticket": entry["ticket"], "name": entry["name"], "state": "queued", "order": order_of(entry)}
            for spec in admissions(entry)]


def admission_blocked(entry, records):
    """A request can make progress once any of its stages could start; the reason names its first stage's wait."""
    reasons = [blocked_reason({**spec, "ticket": "~"}, records) for spec in waiting_admissions(entry)]
    return None if None in reasons else reasons[0]


def batch_prefix(entries, operations):
    """Batch membership is fixed before source capture. Invocations are admitted through their own
    reservations, which `operations` already carries."""
    running = [e for e in entries if e["state"] == "running" and e.get("kind") == "batch"]
    if len(running) >= MAX_ACTIVE_BATCHES:
        return []
    assigned = {member for entry in running for member in entry.get("members", [])}
    requests = [e for e in entries if e.get("kind") == "request" and e["state"] != "running"
                and e["ticket"] not in assigned]
    older = []
    for start, candidate in enumerate(requests):
        if admission_blocked(candidate, [*older, *operations]) is None:
            break
        older.extend(waiting_admissions(candidate))
    else:
        return []
    first = requests[start]
    members = []
    for entry in requests[start:start + MAX_REQUESTS]:
        if entry["compatibility"] != first["compatibility"]:
            break
        members.append(entry)
    return members


def status_details(queue):
    status = queue.status()
    active = [entry for entry in status["runs"] if entry["state"] == "running" and entry.get("kind") == "batch"]
    members = {member for entry in active for member in entry.get("members", [])}
    waiting = [entry for entry in status["runs"] if entry["state"] == "queued" and entry.get("kind") == "request"
               and entry["ticket"] not in members]
    eligible = batch_prefix(status["runs"], status["operations"])
    reservations = {operation.get("batch"): operation for operation in status["operations"]}
    label = lambda entry: f"{entry['name']} ({entry['ticket']})"
    for entry in status["runs"]:
        if entry.get("kind") == "batch":
            entry["progress"] = read_optional(Path(entry["directory"]) / "progress.json")
            continue
        assigned = read_optional(queue.root / (entry["ticket"] + ".batch.json"))
        if assigned:
            entry.update(state="sharing", batch=assigned)
            continue
        if entry["state"] != "queued":
            continue
        if status["paused"]:
            reason = "paused"
        elif entry.get("kind") != "request":
            reason = (reservations.get(entry["ticket"]) or {}).get("blocked_on")
        elif len(active) >= MAX_ACTIVE_BATCHES:
            reason = "active batch limit"
        elif entry not in eligible:
            earlier = waiting[:waiting.index(entry)]
            older = [spec for run in earlier for spec in waiting_admissions(run)]
            reason = admission_blocked(entry, [*older, *status["operations"]])
            if reason is None and earlier:
                reason = "earlier request: " + label(earlier[0])
        else:
            reason = blocked_reason({"ticket": "~", "locks": ["snapshot"], "workers": 1, "order": order_of(entry)},
                                    status["operations"])
        entry["blocked_on"] = reason
    return advisories.observe(queue, annotate(queue, status))


def launch(queue, members, environment, lock_file):
    """Transfer the active lease to a supervisor independent of every subscriber."""
    first = members[0]
    batch_id = uuid.uuid4().hex
    directory = artifact_root(first["source"]) / "verification" / batch_id
    directory.mkdir(parents=True, mode=0o700)
    directory.chmod(0o700)
    ticket = first["ticket"] + "-batch"
    entry = {"kind": "batch", "ticket": ticket, "pid": os.getpid(), "name": "verification batch",
             "phase": "capture",
             "lease_fd": LEASE_FD,
             "source": first["source"], "workers": first["workers"], "state": "running",
             "queued_at": first["queued_at"], "started_at": time.time(), "batch_id": batch_id,
             "members": [member["ticket"] for member in members], "directory": str(directory)}
    lease_path = queue.root / (ticket + ".lease")
    with lease_path.open("x+") as lease:
        lock_file(lease)
        write_lease(lease, entry)
        write_json(directory / "plan.json", {"batch": entry, "requests": members})
        for member in members:
            write_json(queue.root / (member["ticket"] + ".batch.json"), {"directory": str(directory), "ticket": ticket})
        env = foreground_git_environment(
            {**environment, "PW_TEST_EXECUTION_ROOT": str(queue.root), "PW_TEST_EXECUTION_TICKET": ticket})
        options = {"start_new_session": True, "pass_fds": (lease.fileno(),)}
        with (directory / "batch.log").open("ab", buffering=0) as log:
            try:
                child = subprocess.Popen([sys.executable, str(Path(__file__).with_name("test-execution.py")),
                                          "batch-supervise", "--", str(directory), str(lease.fileno())],
                                         cwd=first["source"], env=env, stdin=subprocess.DEVNULL,
                                         stdout=log, stderr=log, **options)
                entry["pid"] = child.pid
                entry["owner_identity"] = identity(child.pid)
                write_lease(lease, entry)
            except BaseException:
                lease_path.unlink(missing_ok=True)
                raise
    return child


def request_admissions(queue, plan, requested):
    """One admission per distinct stage declaration, with the shortest estimate among the stages sharing it."""
    from verification_plan import catalog
    from verification_resources import demand, profile
    data = catalog()
    history = read_history(queue)
    found = {}
    for stage in plan["stages"]:
        spec = profile(stage, data)
        admission = {"locks": spec["locks"], "shared_locks": spec.get("shared_locks", []),
                     "workers": demand(spec, requested), "estimate": estimate(history, stage["name"])}
        key = json.dumps({k: admission[k] for k in ("locks", "shared_locks", "workers")}, sort_keys=True)
        known = found.get(key)
        if known is None or (admission["estimate"] is not None
                             and (known["estimate"] is None or admission["estimate"] < known["estimate"])):
            found[key] = admission
    return list(found.values())


def subscribe(queue, lease_class, lock_file, root, binary, arguments, plan, environment):
    from verification_reuse import environment_identity
    requested = int(environment["PW_TEST_WORKERS"])
    admissions = request_admissions(queue, plan, requested)
    descriptor = {"kind": "request", "source": str(Path(root).resolve()), "binary": str(Path(binary).resolve()),
                  "arguments": arguments, "plan": plan, "admissions": admissions, "admission": admissions[0],
                  "compatibility": compatibility(root, binary, environment),
                  "environment_identity": environment_identity(environment)}
    name = ", ".join(plan["names"])
    supervisor = None
    assignment = None
    log = None
    last_notice = 0.0
    last_state = None
    with lease_class(queue, name, int(environment["PW_TEST_WORKERS"]), descriptor) as lease:
        print("test execution: wait on this command session for the result; no status polling is needed",
              file=sys.stderr, flush=True)
        assignment_path = queue.root / (lease.entry["ticket"] + ".batch.json")
        try:
            while True:
                with queue.locked():
                    entries = queue.entries()
                    paused = (queue.root / "paused.json").exists()
                    if assignment_path.exists():
                        assignment = read_json(assignment_path)
                    elif not paused:
                        from verification_resources import entries as reservations
                        operations = reservations(queue, lock_file)
                        members = batch_prefix(entries, operations)
                        if members and members[0]["ticket"] == lease.entry["ticket"]:
                            capture = {"ticket": "~", "locks": ["snapshot"], "workers": 1,
                                       "order": order_of(members[0])}
                            if blocked_reason(capture, operations) is None:
                                supervisor = launch(queue, members, environment, lock_file)
                                assignment = read_json(assignment_path)
                                entries = queue.entries()
                if assignment:
                    directory = Path(assignment["directory"])
                    if log is None:
                        print(f"test execution: {name} joined batch {directory.name} (receipt {directory / (lease.entry['ticket'] + '.json')})",
                              file=sys.stderr, flush=True)
                        log = (directory / "batch.log").open("rb")
                    output = log.read()
                    if output:
                        sys.stdout.buffer.write(output)
                        sys.stdout.buffer.flush()
                    receipt = directory / (lease.entry["ticket"] + ".json")
                    if receipt.exists():
                        result = read_json(receipt)
                        print(f"test execution: {name}: {result['status']} (source {result.get('source_commit') or 'uncaptured'}; receipt {receipt})",
                              file=sys.stderr, flush=True)
                        # The receipt keeps the tool's own code; the caller gets
                        # the runner's verdict.
                        return outcome_exit(result["exit_code"], result["status"] != "unverified")
                    if not any(e["ticket"] == assignment["ticket"] for e in entries):
                        result = unfinished_receipt(directory, lease.entry, "Batch exited without completing this request")
                        write_json(receipt, result)
                        print(f"test execution: {result['reason']}; receipt {receipt}", file=sys.stderr)
                        return 2
                state = "sharing active verification" if assignment else ("paused" if paused else "waiting for admission")
                if state != last_state:
                    print(f"test execution: {name}: {state}", file=sys.stderr, flush=True)
                    last_state = state
                if time.monotonic() - last_notice >= 30:
                    report(queue, lease.entry["ticket"])
                    last_notice = time.monotonic()
                time.sleep(0.25)
        finally:
            if log:
                log.close()
            assignment_path.unlink(missing_ok=True)
            if supervisor is not None:
                # Polling reaps completed supervisors without stopping active batches.
                supervisor.poll()


def stop_child(child):
    if child.poll() is not None:
        return
    if os.name == "nt":
        subprocess.run(["taskkill", "/PID", str(child.pid), "/T", "/F"], capture_output=True, check=False)
    else:
        try:
            os.killpg(child.pid, signal.SIGTERM)
        except ProcessLookupError:
            child.wait()
            return
    try:
        child.wait(timeout=10)
    except subprocess.TimeoutExpired:
        if os.name == "nt":
            child.kill()
        else:
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
        child.wait()


def child_options():
    if os.name == "nt":
        return {}
    descriptors = []
    candidates = [int(os.environ.get("PW_TEST_EXECUTION_FD", LEASE_FD))]
    if os.environ.get("PW_TEST_RESOURCE_FD"):
        candidates.append(int(os.environ["PW_TEST_RESOURCE_FD"]))
    if os.environ.get("PW_SOURCE_SNAPSHOT_COMMIT"):
        candidates.extend([7, 8])
    for fd in candidates:
        try:
            os.fstat(fd)
        except OSError:
            continue
        descriptors.append(fd)
    return {"start_new_session": True, "pass_fds": tuple(descriptors)}


def inherited_lease(queue, entry, lock_file):
    """A held open-file description survives reparenting after a supervisor crash."""
    if os.name == "nt":
        return False
    try:
        descriptor = entry.get("lease_fd", LEASE_FD)
        held = os.fstat(descriptor)
        expected = (queue.root / (entry["ticket"] + ".lease")).stat()
        if (held.st_dev, held.st_ino) != (expected.st_dev, expected.st_ino):
            return False
        with os.fdopen(os.dup(descriptor), "r+") as lease:
            lock_file(lease, blocking=False)
        return True
    except OSError:
        return False


def supervise(queue, directory, inherited_fd):
    directory = Path(directory)
    plan = read_json(directory / "plan.json")
    entry = plan["batch"]
    os.dup2(inherited_fd, LEASE_FD, inheritable=True)
    if inherited_fd != LEASE_FD:
        os.close(inherited_fd)
    lease = os.fdopen(LEASE_FD, "r+")
    os.environ["PW_TEST_EXECUTION_FD"] = str(LEASE_FD)
    child = None
    try:
        with queue.locked():
            entry["pid"] = os.getpid()
            entry["owner_identity"] = owner_identity(os.getpid())
            entry["phase"] = "capture"
            write_lease(lease, entry)
        print(f"test execution: admitted batch {entry['batch_id']} ({len(plan['requests'])} requests, worker budget {entry['workers']})", flush=True)
        command = ["bash", "scripts/test-source-snapshot.sh", "run", "--", sys.executable,
                   "scripts/test-execution.py", "batch-execute", "--", str(directory)]
        child = subprocess.Popen(command, cwd=entry["source"], **child_options())
        with queue.locked():
            lease.seek(0)
            entry = json.load(lease)
            entry["child_pid"] = child.pid
            entry["child_identity"] = identity(child.pid)
            write_lease(lease, entry)
        while child.poll() is None:
            completed = all((directory / (member + ".json")).exists() for member in entry["members"])
            if not completed and not active_members(queue, entry["members"]):
                stop_child(child)
                break
            time.sleep(0.1)
        return child.wait()
    finally:
        if child is not None:
            stop_child(child)
        for member in plan["requests"]:
            receipt = directory / (member["ticket"] + ".json")
            if not receipt.exists():
                write_json(receipt, unfinished_receipt(directory, member,
                                                       "Verification interrupted or source capture failed"))
        write_json(directory / "finished.json", {"finished_at": time.time()})
        # Descendants retain admission after supervisor exit.
        lease.close()
        with queue.locked():
            queue.entries()
        prune(queue, Path(entry["source"]))


def prune(queue, root):
    root = root.resolve()
    directory = artifact_root(root) / "verification"
    if not directory.is_dir():
        return
    with queue.locked():
        live = queue.entries()
        protected = {e.get("directory") for e in live}
        live_requests = {e["ticket"] for e in live}
        batches = sorted((p for p in directory.iterdir() if (p / "finished.json").is_file()),
                         key=lambda p: (p / "finished.json").stat().st_mtime, reverse=True)
        retained_bytes = 0
        for index, old in enumerate(batches):
            plan = read_json(old / "plan.json")
            size = sum(p.stat().st_size for p in old.rglob("*") if p.is_file())
            in_use = str(old) in protected or live_requests.intersection(plan["batch"]["members"])
            if in_use or index == 0 or (index < KEEP_BATCHES and retained_bytes + size <= MAX_RETAINED_BYTES):
                retained_bytes += size
                continue
            subprocess.run(["git", "-C", str(root), "update-ref", "-d", "refs/verification/batches/" + old.name],
                           check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            shutil.rmtree(old)

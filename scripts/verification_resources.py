"""CPU reservations and exclusive resource leases for verification operations."""

import json
import os
import shlex
import time
import uuid

from verification_health import owner_identity
from verification_history import estimate, read_history

if os.name != "nt":
    import fcntl


def dedicated_host():
    """A dedicated host, such as a hosted CI runner, has no neighboring work to leave room for."""
    return os.environ.get("PW_TEST_HOST", "shared") == "dedicated"


def capacity():
    return worker_budget({})


def worker_budget(environment):
    cpus = os.cpu_count() or 1
    limit = min(8, max(1, cpus if dedicated_host() else cpus * 3 // 4))
    requested = int(environment.get("PW_TEST_WORKERS", limit))
    if not 1 <= requested <= limit:
        raise ValueError(f"PW_TEST_WORKERS must be between 1 and {limit} (the shared worker capacity)")
    return requested


def bounded_environment(environment, workers):
    env = dict(environment)
    per_process = int(env.get("GOMAXPROCS", 1))
    requested_packages = int(env.get("GO_TEST_P", workers))
    if per_process < 1 or requested_packages < 1:
        raise ValueError("GOMAXPROCS and GO_TEST_P must be positive")
    packages = min(requested_packages, max(1, workers // min(per_process, workers)))
    limits = {"GO_TEST_P": packages, "GO_TEST_PARALLEL": max(1, workers // packages),
              "GOMAXPROCS": max(1, workers // packages),
              "PW_VITEST_MAX_WORKERS": min(4, workers), "PW_FUZZ_WORKERS": packages,
              "CARGO_BUILD_JOBS": workers, "RUST_TEST_THREADS": workers}
    for name, limit in limits.items():
        value = int(env.get(name, limit))
        if value < 1:
            raise ValueError(f"{name} must be positive")
        env[name] = str(min(value, limit))
    flags = shlex.split(env.get("GOFLAGS", ""))
    result = []
    skip = False
    for flag in flags:
        if skip:
            skip = False
        elif flag == "-p":
            skip = True
        elif not flag.startswith("-p="):
            result.append(flag)
    env["GOFLAGS"] = shlex.join([*result, f"-p={env['GO_TEST_P']}"])
    env["PW_TEST_WORKERS"] = str(workers)
    return env


EXCLUSIVE = {"locks": ["*"], "shared_locks": [], "workers": "all"}


def profile(stage, catalog):
    name = ("go:bundled" if stage.get("bundled") else "go") if stage["kind"] == "go" else stage["name"]
    return catalog["resources"].get(name, EXCLUSIVE)


def demand(spec, requested):
    limit = capacity()
    workers = spec["workers"]
    if workers == "shared":
        workers = limit if dedicated_host() else max(1, limit // 2)
    elif workers == "all":
        workers = limit
    return min(requested, workers, limit)


def combine(specs):
    """One reservation covering several declarations: every lock, shared only where all holders share it."""
    locks = sorted({lock for spec in specs for lock in spec["locks"]})
    shared = [lock for lock in locks
              if all(lock in spec.get("shared_locks", []) for spec in specs if lock in spec["locks"])]
    limit = capacity()
    return {"locks": locks, "shared_locks": shared,
            "workers": max((demand(spec, limit) for spec in specs), default=limit)}


def resource_conflict(left, right):
    common = set(left["locks"]).intersection(right["locks"])
    shared = set(left.get("shared_locks", [])).intersection(right.get("shared_locks", []))
    return "*" in left["locks"] or "*" in right["locks"] or bool(common - shared)


def order_of(entry):
    """Reservations and leases share one arrival order; tickets from the two sources do not compare."""
    if type(entry.get("order")) is int:
        return entry["order"]
    if entry.get("queued_at"):
        return int(entry["queued_at"] * 1e9)
    try:
        return int(entry["ticket"].split("-", 1)[0])
    except (KeyError, ValueError):
        return 0


def lease_operations(queue):
    """Admission held or awaited through a lease rather than a reservation file."""
    result = []
    for entry in queue.entries():
        kind = entry.get("kind")
        if kind == "batch":
            if entry["state"] == "running" and entry.get("phase") == "capture":
                result.append({"ticket": entry["ticket"], "name": "Source capture", "workers": 1,
                               "state": "running", "locks": ["snapshot"], "batch": entry["ticket"],
                               "order": order_of(entry), "started_at": entry.get("started_at")})
        elif kind not in {"request", "invocation"}:
            # A lease that declares nothing holds, or awaits, the whole host.
            result.append({"ticket": entry["ticket"], "name": entry["name"], "state": entry["state"],
                           "workers": entry.get("workers", capacity()), "locks": ["*"],
                           "batch": entry["ticket"], "order": order_of(entry),
                           "started_at": entry.get("started_at")})
    return result


def entries(queue, lock_file):
    result = []
    for path in queue.root.glob("*.resource"):
        with path.open("r+") as lease:
            try:
                lock_file(lease, blocking=False)
            except BlockingIOError:
                result.append(json.load(lease))
            else:
                path.unlink()
    result.extend(lease_operations(queue))
    return sorted(result, key=lambda entry: (order_of(entry), entry["ticket"]))


def expected_end(entry, now):
    """A running operation that has outlived its estimate has no predictable end."""
    if entry.get("estimate") is None or not entry.get("started_at"):
        return None
    end = entry["started_at"] + entry["estimate"]
    return end if end > now else None


def shadow_start(waiter, running, now):
    """The earliest time the running work lets `waiter` start, or None when that cannot be predicted."""
    start = now
    for entry in running:
        if resource_conflict(waiter, entry):
            end = expected_end(entry, now)
            if end is None:
                return None
            start = max(start, end)
    free = capacity() - sum(entry["workers"] for entry in running)
    if free >= waiter["workers"]:
        return start
    for entry in sorted(running, key=lambda item: expected_end(item, now) or float("inf")):
        end = expected_end(entry, now)
        if end is None:
            return None
        free += entry["workers"]
        if free >= waiter["workers"]:
            return max(start, end)
    return None


def precedes(entry, candidate):
    """Work is ordered by when the request it serves arrived; a candidate without an arrival is last."""
    if entry["ticket"] == candidate["ticket"]:
        return False
    if "order" not in candidate:
        return True
    return (order_of(entry), entry["ticket"]) < (candidate["order"], candidate["ticket"])


def blocked_reason(candidate, records, now=None):
    """Admission is FIFO per conflict. Later work passes an earlier waiter it would delay only when its
    estimated completion falls before that waiter could start anyway."""
    now = time.time() if now is None else now
    running = [entry for entry in records if entry["state"] == "running"]
    used = sum(entry["workers"] for entry in running)
    for entry in running:
        if resource_conflict(candidate, entry):
            return f"resource held by {entry['name']} ({entry['ticket']})"
    if used + candidate["workers"] > capacity():
        return "worker capacity"
    estimate = candidate.get("estimate")
    for entry in sorted(records, key=lambda item: (order_of(item), item["ticket"])):
        if entry["state"] != "queued" or not precedes(entry, candidate) or not (
                resource_conflict(candidate, entry) or used + entry["workers"] > capacity()):
            continue
        start = shadow_start(entry, running, now)
        if estimate is None or start is None or now + estimate > start:
            return f"earlier operation: {entry['name']} ({entry['ticket']})"
    return None


class Reservation:
    def __init__(self, queue, lock_file, batch, name, spec, requested, order=None):
        """`order` is the arrival of the request this work serves; it defaults to now."""
        self.queue = queue
        self.lock_file = lock_file
        created = time.time_ns()
        self.entry = {"ticket": f"{created:020d}-{uuid.uuid4().hex}",
                      "order": created if order is None else order, "batch": batch,
                      "name": name, "locks": spec["locks"], "workers": demand(spec, requested),
                      "shared_locks": spec.get("shared_locks", []),
                      "estimate": estimate(read_history(queue), name),
                      "state": "queued", "pid": os.getpid(), "owner_identity": owner_identity(os.getpid())}
        self.path = queue.root / (self.entry["ticket"] + ".resource")
        self.file = None

    def write(self):
        body = json.dumps(self.entry)
        body = body.ljust(max(len(body), os.fstat(self.file.fileno()).st_size))
        self.file.seek(0)
        self.file.write(body)
        self.file.flush()

    def __enter__(self):
        with self.queue.locked():
            self.file = self.path.open("x+")
            self.lock_file(self.file)
            if os.name != "nt":
                descriptor = fcntl.fcntl(self.file.fileno(), fcntl.F_DUPFD, 201)
                original = self.file
                self.file = os.fdopen(descriptor, "r+")
                original.close()
            self.write()
        return self

    def try_acquire(self):
        """One admission attempt; the reservation stays queued when blocked."""
        with self.queue.locked():
            reason = blocked_reason(self.entry, entries(self.queue, self.lock_file))
            self.entry["blocked_on"] = reason
            if reason is None:
                self.entry.update(state="running", started_at=time.time())
            self.write()
            return reason is None

    def acquire(self, interested):
        while interested():
            if self.try_acquire():
                return True
            time.sleep(0.1)
        return False

    def __exit__(self, *_):
        with self.queue.locked():
            # A surviving child keeps the reservation until its inherited fd closes.
            self.file.close()
            entries(self.queue, self.lock_file)

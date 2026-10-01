"""Advisory queue observations: who is held up, for how long, and whether the result still applies."""

import hashlib
from pathlib import Path
import subprocess
import sys
import time

from verification_history import read_history, samples, typical
from verification_state import read_state, write_json

BLOCKED_SECONDS = 900
RUNTIME_FACTOR = 3
RUNTIME_FLOOR_SECONDS = 300
STAMP_SECONDS = 30
STAMP_RETAIN_SECONDS = 3600
NOTICE_REPEAT_SECONDS = 600
TRAILER = "Advisory only; admission is unchanged. Inspect ./task test:status."

_announced = {}


def duration_text(seconds):
    if seconds < 90:
        return f"{round(seconds)} s"
    minutes = round(seconds / 60)
    return f"{minutes} min" if minutes < 90 else f"{minutes // 60} h {minutes % 60} min"


def plural(count, noun):
    return f"{count} {noun}" if count == 1 else f"{count} {noun}s"


def source_stamp(root):
    """The content an agent wants verified: the commit plus every uncommitted difference."""
    git = ["git", "--no-optional-locks", "-C", str(root)]
    try:
        head = subprocess.run([*git, "rev-parse", "HEAD"], capture_output=True, timeout=60)
        changes = subprocess.run([*git, "status", "--porcelain", "-z", "--untracked-files=normal"],
                                 capture_output=True, timeout=300)
    except (OSError, subprocess.TimeoutExpired):
        return None
    if head.returncode or changes.returncode:
        return None
    return hashlib.sha256(head.stdout + b"\0" + changes.stdout).hexdigest()


def observed_stamps(queue, roots, now):
    """One host-wide sample per interval; git does not run once per observer."""
    path = queue.root / "source-stamps.json"
    with queue.locked():
        stamps = read_state(path)
        claimed = [root for root in roots if now - stamps.get(root, {}).get("sampled_at", 0) >= STAMP_SECONDS]
        for root in claimed:
            stamps.setdefault(root, {})["sampled_at"] = now
        if claimed:
            write_json(path, stamps)
    sampled = {root: source_stamp(root) for root in claimed}
    with queue.locked():
        stamps = read_state(path)
        for root, value in sampled.items():
            stamps.setdefault(root, {}).update(stamp=value, sampled_at=now)
        expired = [root for root, record in stamps.items()
                   if root not in roots and now - record.get("sampled_at", 0) > STAMP_RETAIN_SECONDS]
        for root in expired:
            del stamps[root]
        if sampled or expired:
            write_json(path, stamps)
        return {root: stamps.get(root, {}).get("stamp") for root in roots}


def advisory(entry, code, summary, **facts):
    entry.setdefault("advisories", []).append({"code": code, "summary": summary, "action": "none", **facts})


def waiting_requests(status):
    """Requests a running batch already serves are being verified, not held up."""
    served = {member for entry in status.get("runs", [])
              if entry["state"] == "running" for member in entry.get("members", [])}
    return [entry for entry in status.get("runs", [])
            if entry["state"] == "queued" and entry["ticket"] not in served]


def backlog_facts(waiting, now):
    oldest = min((entry["queued_at"] for entry in waiting if entry.get("queued_at")), default=None)
    return {"waiting": len(waiting),
            "oldest_wait_seconds": round(max(0.0, now - oldest), 1) if oldest else 0.0}


def elapsed_fields(status, waiting, now):
    for entry in [*status.get("runs", []), *status.get("operations", [])]:
        if entry["state"] == "running" and entry.get("started_at"):
            entry["elapsed_seconds"] = round(max(0.0, now - entry["started_at"]), 1)
    for entry in waiting:
        if entry.get("queued_at"):
            entry["waited_seconds"] = round(max(0.0, now - entry["queued_at"]), 1)


def runtime_advisories(queue, status):
    history = read_history(queue)
    for entry in [*status.get("runs", []), *status.get("operations", [])]:
        elapsed = entry.get("elapsed_seconds")
        usual = typical(history, entry["name"])
        if entry.get("kind") == "batch" or elapsed is None or usual is None:
            continue
        if elapsed < RUNTIME_FLOOR_SECONDS or elapsed < RUNTIME_FACTOR * usual:
            continue
        seen = len(samples(history, entry["name"]))
        advisory(entry, "runtime_exceeds_history",
                 f"running {duration_text(elapsed)}; the last {seen} runs of {entry['name']} "
                 f"took about {duration_text(usual)}",
                 elapsed_seconds=elapsed, typical_seconds=round(usual, 1), samples=seen)


def exclusive_run(entry):
    """A run outside a batch that declares nothing holds every resource."""
    if entry["state"] != "running" or entry.get("kind") in {"batch", "request"}:
        return False
    return "*" in (entry.get("admission") or {"locks": ["*"]})["locks"]


def holding_advisory(status, waiting, facts):
    """Only exclusive admission serializes every checkout, and a run that just started did not cause older waits."""
    exclusive = next((entry for entry in status.get("runs", []) if exclusive_run(entry)), None)
    if exclusive is None or not waiting or facts["oldest_wait_seconds"] < BLOCKED_SECONDS:
        return
    elapsed = exclusive.get("elapsed_seconds")
    if elapsed is None or elapsed < BLOCKED_SECONDS:
        return
    advisory(exclusive, "holding_queue",
             f"holding exclusive admission for {duration_text(elapsed)}; "
             f"{plural(facts['waiting'], 'request')} cannot start, the oldest queued "
             f"{duration_text(facts['oldest_wait_seconds'])} ago",
             workers=exclusive.get("workers"), **facts)


def source_advisories(queue, status, now):
    batches = [entry for entry in status.get("runs", [])
               if entry["state"] == "running" and entry.get("kind") == "batch" and entry.get("directory")]
    roots = sorted({entry["source"] for entry in batches})
    if not roots:
        return
    stamps = observed_stamps(queue, roots, now)
    for entry in batches:
        captured = read_state(Path(entry["directory"]) / "source.json").get("source_stamp")
        live = stamps.get(entry["source"])
        if captured and live and captured != live:
            advisory(entry, "source_superseded",
                     f"{entry['source']} changed after this batch captured its source; these receipts "
                     "will not cover the current tree",
                     source_root=entry["source"])


def queue_advisories(status, waiting, facts):
    if not waiting or facts["oldest_wait_seconds"] < BLOCKED_SECONDS:
        return []
    wait = duration_text(facts["oldest_wait_seconds"])
    if status.get("paused"):
        reason = (status["paused"] or {}).get("reason", "paused")
        return [{"code": "paused_backlog", "action": "none", **facts,
                 "summary": f"{plural(facts['waiting'], 'request')} have waited up to {wait} while the queue "
                            f"is paused ({reason})"}]
    capacity = status.get("capacity", {})
    return [{"code": "queue_backlog", "action": "none", **facts,
             "summary": f"{plural(facts['waiting'], 'request')} waiting, the oldest for {wait}; "
                        f"{capacity.get('available', 0)} of {capacity.get('workers', 0)} workers available"}]


def summary_text(status, waiting, facts):
    capacity = status.get("capacity", {})
    running = [entry for entry in status.get("runs", []) if entry["state"] == "running"]
    parts = [f"{capacity.get('reserved', 0)} of {capacity.get('workers', 0)} workers reserved",
             f"{len(running)} running", f"{facts['waiting']} waiting"]
    if waiting:
        parts.append(f"oldest queued {duration_text(facts['oldest_wait_seconds'])} ago")
    if status.get("paused"):
        parts.append("paused")
    return "; ".join(parts)


def observe(queue, status, now=None):
    """Derived queue facts an agent weighs; nothing here changes admission."""
    now = time.time() if now is None else now
    waiting = waiting_requests(status)
    facts = backlog_facts(waiting, now)
    elapsed_fields(status, waiting, now)
    runtime_advisories(queue, status)
    holding_advisory(status, waiting, facts)
    source_advisories(queue, status, now)
    status["advisories"] = queue_advisories(status, waiting, facts)
    status["summary"] = summary_text(status, waiting, facts)
    return status


def concerns(entry, item, ticket):
    """A shared checkout supersedes every capture; only the requester waiting on one is told."""
    if item["code"] != "source_superseded":
        return True
    return ticket is not None and ticket in entry.get("members", [])


def notices(status, ticket=None):
    for item in status.get("advisories", []):
        yield "queue", item["code"], f"test execution: queue: {item['code']}: {item['summary']}. {TRAILER}"
    for entry in [*status.get("runs", []), *status.get("operations", [])]:
        for item in entry.get("advisories", []):
            if not concerns(entry, item, ticket):
                continue
            yield (entry["ticket"], item["code"],
                   f"test execution: {entry['name']} ({entry['ticket']}): {item['code']}: "
                   f"{item['summary']}. {TRAILER}")


def announce(subject, code, text, now=None):
    """A standing condition is said once, then at most once per repeat interval."""
    now = time.monotonic() if now is None else now
    last = _announced.get((subject, code))
    if last is not None and now - last < NOTICE_REPEAT_SECONDS:
        return False
    _announced[(subject, code)] = now
    print(text, file=sys.stderr, flush=True)
    return True


def admission_notice(queue, status, name, reservation):
    """Said before the wait: what this run outside a shared batch will hold, and for about how long."""
    workers = reservation["workers"]
    if "*" in reservation["locks"]:
        facts = [f"no other verification runs while it holds its {workers}-worker admission"]
        heading = f"test execution: {name} takes exclusive admission: "
    else:
        locks = ", ".join(reservation["locks"]) or "no locks"
        facts = [f"it holds {locks} and {plural(workers, 'worker')} for its whole run; other work continues"]
        heading = f"test execution: {name} runs outside a shared batch: "
    usual = typical(read_history(queue), name)
    if usual:
        facts.append(f"recent runs took about {duration_text(usual)}")
    waiting = waiting_requests(status)
    if waiting:
        facts.append(f"{plural(len(waiting), 'request')} already queued")
    return heading + "; ".join(facts)

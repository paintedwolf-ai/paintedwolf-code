"""Hosted runner priority: the merge queue and releases, then ready pull requests, drafts, main cache warming, and background work.

Hosted runners start jobs first come, first served. Each sweep reads run, job, branch, and pull request
facts, cancels the lowest-priority runs only as far as waiting merge-queue and release jobs need runners,
and re-runs the cancelled jobs of preempted work once nothing that outranks it is waiting.
"""

from collections import Counter, namedtuple
from datetime import datetime, timedelta, timezone
from pathlib import PurePosixPath
import subprocess

from verification_plan import catalog


# The GitHub Free plan runs twenty jobs at once across the organization, five of them on macOS.
LIMITS = {"total": 20, "macos": 5}
# Other repositories share the plan, so a protected job queued this long is starved whatever this repository holds.
STARVED_AFTER = timedelta(minutes=5)
# Preempted work resumes while its run is below this attempt; past it, the next push or schedule carries the work.
ATTEMPTS = 5
QUEUE_BRANCHES = "gh-readonly-queue/"
# Run states that still hold, or will claim, a runner.
UNFINISHED_RUNS = ("requested", "waiting", "pending", "queued", "in_progress")
PLATFORMS = ("macos", "linux")
QUEUE, RELEASE, READY, DRAFT, WARMING, BACKGROUND = "queue", "release", "ready", "draft", "warming", "background"
QUALIFICATION = "qualification"
PROTECTED = {QUEUE, RELEASE}
# Lowest priority first: the order in which runs give up runners.
YIELD_ORDER = (BACKGROUND, WARMING, DRAFT, READY, QUALIFICATION)
# The event whose newest run resumes; runs started by hand are re-run by whoever started them.
RESUMED_EVENTS = {WARMING: "push", QUALIFICATION: "push", BACKGROUND: "schedule"}

Plan = namedtuple("Plan", "stale preempted resumable")


def workflow_classes():
    """Workflow file to priority class, from the catalog; CI's class follows each run's event."""
    declared = catalog()["runner_priority"]
    if set(declared) - {RELEASE, QUALIFICATION, WARMING, BACKGROUND}:
        raise ValueError("runner priority classes are release, qualification, warming, and background")
    files = [name for names in declared.values() for name in names]
    if len(files) != len(set(files)) or "ci.yml" in files:
        raise ValueError("each workflow declares one runner priority, and CI's follows its event")
    return {name: kind for kind, names in declared.items() for name in names}


def run_class(run, classes, ready_heads):
    """The run's priority class, or None for a run the scheduler never cancels."""
    workflow = PurePosixPath(run["path"].split("@")[0]).name
    if workflow == "ci.yml":
        ready = READY if run["head_sha"] in ready_heads else DRAFT
        return {"merge_group": QUEUE, "pull_request": ready}.get(run["event"])
    # Each issue event's run handles one issue, so no newer run would carry it after a cancellation.
    if run["event"] == "issues":
        return None
    return classes.get(workflow)


def platform(job):
    return "macos" if any(label.startswith("macos") for label in job["labels"]) else "linux"


def count(jobs, status):
    counts = Counter(platform(job) for job in jobs if job["status"] == status)
    return {name: counts[name] for name in PLATFORMS}


def timestamp(value):
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def shortfall(protected, everything, now):
    """Runners each platform must free for its waiting merge-queue and release jobs."""
    running, queued = count(everything, "in_progress"), count(everything, "queued")
    busy = sum(running.values())
    free = {"macos": min(LIMITS["macos"] - running["macos"], LIMITS["total"] - busy), "linux": LIMITS["total"] - busy}
    need = {}
    for name in PLATFORMS:
        waiting = [job for job in protected if job["status"] == "queued" and platform(job) == name]
        # Jobs queued beyond the free runners wait, whoever owns them.
        crowded = min(len(waiting), max(0, queued[name] - max(0, free[name])))
        starved = sum(now - timestamp(job["created_at"]) >= STARVED_AFTER for job in waiting)
        need[name] = max(crowded, starved)
    return need


def preemptions(candidates, need):
    """Runs in yield order until the runners they free cover each waiting platform's need.

    A run is the unit of cancellation, so a run chosen for one platform also frees its others. A run that
    holds nothing but waits on a needy platform is chosen too, since it would take the next free runner.
    """
    chosen = []
    for run, jobs in candidates:
        needy = [name for name in PLATFORMS if need[name] > 0]
        if not needy:
            break
        holding, waiting = count(jobs, "in_progress"), count(jobs, "queued")
        competes = not any(holding.values()) and any(waiting[name] for name in needy)
        if competes or any(holding[name] for name in needy):
            chosen.append(run)
            need = {name: need[name] - holding[name] for name in PLATFORMS}
    return chosen


def decide(runs, kinds, jobs, live_groups, now):
    """Runs to cancel, and the classes whose preempted work may resume now."""
    stale = [run for run in runs if kinds[run["id"]] == QUEUE and run["head_branch"] not in live_groups]
    live = [run for run in runs if run not in stale]
    protected = [job for run in live if kinds[run["id"]] in PROTECTED for job in jobs.get(run["id"], [])]
    candidates = sorted((run for run in live if kinds[run["id"]] in YIELD_ORDER),
                        key=lambda run: (run["created_at"], run["id"]), reverse=True)
    candidates.sort(key=lambda run: YIELD_ORDER.index(kinds[run["id"]]))
    need = shortfall(protected, [job for run in live for job in jobs.get(run["id"], [])], now)
    # Background work runs only while the merge queue is empty.
    yielded = [run for run in candidates if kinds[run["id"]] == BACKGROUND and live_groups]
    for run in yielded:
        held = count(jobs.get(run["id"], []), "in_progress")
        need = {name: need[name] - held[name] for name in PLATFORMS}
    preempted = yielded + preemptions([(run, jobs.get(run["id"], [])) for run in candidates if run not in yielded], need)
    if any(job["status"] == "queued" for job in protected):
        resumable = set()
    else:
        resumable = {READY, WARMING, QUALIFICATION} | (set() if live_groups else {BACKGROUND})
    return Plan(stale, [(run, kinds[run["id"]]) for run in preempted], resumable)


def resumptions(pull_runs, stream_runs, ready_heads, resumable):
    """Cancelled newest runs of current work: a ready pull request's head, main's latest warming, each latest schedule."""
    chosen = []
    if READY in resumable:
        newest = {}
        for run in sorted(pull_runs, key=lambda run: (run["created_at"], run["id"]), reverse=True):
            newest.setdefault(run["head_sha"], run)
        chosen += [run for sha, run in newest.items() if sha in ready_heads]
    for kind in sorted(resumable & set(RESUMED_EVENTS)):
        chosen += stream_runs.get(kind, [])
    return [run for run in chosen if run["status"] == "completed" and run["conclusion"] == "cancelled"
            and run["run_attempt"] < ATTEMPTS]


def act(github, path, message):
    try:
        github(path, method="POST")
    except subprocess.CalledProcessError as error:
        print(f"::warning::{message} failed: {error.stderr or error}", flush=True)
        return False
    print(message, flush=True)
    return True


def schedule(repository, github, now=None):
    """Prune dead merge groups, preempt for waiting protected jobs, and resume preempted work."""
    now = now or datetime.now(timezone.utc)
    classes = workflow_classes()
    runs = list({run["id"]: run for status in UNFINISHED_RUNS
                 for run in github(f"repos/{repository}/actions/runs", status=status, per_page=100)["workflow_runs"]}.values())
    # Listed after the runs, so a group created in between counts as live.
    live_groups = {ref["ref"].removeprefix("refs/heads/")
                   for ref in github(f"repos/{repository}/git/matching-refs/heads/{QUEUE_BRANCHES}")}
    pulls = github(f"repos/{repository}/pulls", state="open", per_page=100)
    ready_heads = {pull["head"]["sha"] for pull in pulls if not pull["draft"]}
    kinds = {run["id"]: run_class(run, classes, ready_heads) for run in runs}

    def load(selected):
        return {run["id"]: github(f"repos/{repository}/actions/runs/{run['id']}/jobs",
                                  filter="latest", per_page=100)["jobs"] for run in selected}

    live = [run for run in runs if kinds[run["id"]] != QUEUE or run["head_branch"] in live_groups]
    jobs = load(run for run in live if kinds[run["id"]] in PROTECTED)
    if any(job["status"] == "queued" for values in jobs.values() for job in values):
        jobs.update(load(run for run in live if run["id"] not in jobs))
    plan = decide(runs, kinds, jobs, live_groups, now)
    cancelled = []
    # A plain cancel still schedules always() steps on fresh runners; force-cancel releases them at once.
    for run in plan.stale:
        if act(github, f"repos/{repository}/actions/runs/{run['id']}/force-cancel",
               f"cancelled run {run['id']}: merge group {run['head_branch']} no longer exists"):
            cancelled.append(run["id"])
    for run, kind in plan.preempted:
        if act(github, f"repos/{repository}/actions/runs/{run['id']}/force-cancel",
               f"cancelled run {run['id']} ({kind}): its runners go to the merge queue and releases"):
            cancelled.append(run["id"])
    pull_runs = []
    if READY in plan.resumable:
        pull_runs = github(f"repos/{repository}/actions/workflows/ci.yml/runs",
                           event="pull_request", per_page=100)["workflow_runs"]
    stream_runs = {kind: [run for name, declared in classes.items() if declared == kind
                          for run in github(f"repos/{repository}/actions/workflows/{name}/runs",
                                            event=event, per_page=1)["workflow_runs"]]
                   for kind, event in RESUMED_EVENTS.items() if kind in plan.resumable}
    resumed = [run["id"] for run in resumptions(pull_runs, stream_runs, ready_heads, plan.resumable)
               if act(github, f"repos/{repository}/actions/runs/{run['id']}/rerun-failed-jobs",
                      f"resumed run {run['id']} from attempt {run['run_attempt']}")]
    return cancelled, resumed

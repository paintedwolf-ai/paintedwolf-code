"""Hosted lane execution, resource evidence, and queue cleanup."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
import time

from artifact_paths import artifact_root
from ci_policy.evidence import oom_events, classify
from ci_policy import hosted_plan, timings, verification_report

ROOT = Path(__file__).resolve().parents[2]


def task_json(arguments):
    """Run a queue control through ./task and parse the JSON it prints after Task's command echo."""
    result = subprocess.run(["./task", *arguments], cwd=ROOT, capture_output=True, text=True)
    output = result.stdout[result.stdout.find("{"):] if "{" in result.stdout else ""
    return result.returncode, json.loads(output) if output else None


def release_leftover_requests(step):
    """A hosted runner serves one job; withdraw its abandoned queue requests."""
    if os.environ.get("GITHUB_ACTIONS") != "true":
        raise ValueError("release withdraws every queued request, which is only this job's own on a hosted runner")
    _, status = task_json(["test:status"])
    requests = [entry for entry in status["runs"] if entry.get("kind") == "request"]
    unreleased = []
    for entry in requests:
        code, value = task_json(["test:cancel", "--", entry["ticket"], "--force",
                                 "--reason", f"{entry['name']} outlived the {step} step"])
        print(f"cancelled {entry['name']} ({entry['ticket']}): {json.dumps(value, sort_keys=True)}", flush=True)
        if code != 0:
            unreleased.append(entry["ticket"])
    return unreleased


def resource_line():
    """Memory, disk, and the largest processes, for diagnosing a runner that disappears mid-lane."""
    meminfo = dict(line.split(":", 1) for line in Path("/proc/meminfo").read_text().splitlines())
    mib = {key: int(meminfo[key].split()[0]) // 1024 for key in ("MemAvailable", "SwapFree")}
    disks = {where: shutil.disk_usage(where).free // 2**30 for where in sorted({"/", tempfile.gettempdir()})}
    processes = []
    for status in Path("/proc").glob("[0-9]*/status"):
        try:
            fields = dict(line.split(":", 1) for line in status.read_text().splitlines() if ":" in line)
        except OSError:
            continue
        if "VmRSS" in fields:
            processes.append((int(fields["VmRSS"].split()[0]) // 1024, fields["Name"].strip(), status.parent.name))
    top = ", ".join(f"{name}[{pid}] {rss} MiB" for rss, name, pid in sorted(processes, reverse=True)[:4])
    free = ", ".join(f"{where} {gib} GiB" for where, gib in disks.items())
    return (f"runner resources: available {mib['MemAvailable']} MiB, swap free {mib['SwapFree']} MiB; "
            f"disk free {free}; largest {top}")


def sample_resources(stop, interval=60):
    while not stop.wait(interval):
        print(resource_line(), flush=True)


def run_lane(name, shard=""):
    lane = hosted_plan.lanes()[name]
    count = lane.get("shards", 1)
    if (shard == "") != (count == 1) or shard and shard not in {f"{k}/{count}" for k in range(1, count + 1)}:
        raise ValueError(f"CI lane {name} has {count} shard(s); got {shard!r}")
    targets = lane["targets"]
    scope = None
    if os.environ.get("PW_CI_AFFECTED") == "1" or "budgets" in targets:
        from ci_policy.impact import change
        scope = change()
    directory = artifact_root(ROOT) / "ci"
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / "run.json"
    record = {"lane": name, "shard": shard, "targets": targets, "started_at": time.time(), "status": "running", "scope": scope}
    record["oom_before"] = oom_events()
    path.write_text(json.dumps(record, indent=2) + "\n")
    # Hosted runners are slower than development hosts; the lane budget, not the local default, bounds Go runs.
    environment = {"PW_GO_TEST_TIMEOUT_SECONDS": str(lane["minutes"] * 60), **os.environ}
    if scope and scope["base"]:
        environment["PW_CHANGE_BASE"] = scope["base"]
    # A lane whose peak memory outgrows the runner caps its parallelism below the CPU count.
    if "workers" in lane:
        environment["PW_TEST_WORKERS"] = str(lane["workers"])
    # Each shard of a split lane runs its slice of every Go stage's packages. The plan job resolved
    # one set of measurements for the run, so the shards partition the selection the same way.
    measured = environment.pop("PW_CI_PACKAGE_TIMINGS", "")
    if shard:
        environment["PW_GO_SHARD"] = shard
        if measured:
            weights = directory / "package-weights.json"
            weights.write_text(measured)
            environment["PW_GO_TIMINGS"] = str(weights)
    # A hosted runner that runs out of memory or disk vanishes without evidence; the live log keeps these lines.
    stop = threading.Event()
    if os.environ.get("PW_TEST_HOST") == "dedicated" and Path("/proc/meminfo").exists():
        threading.Thread(target=sample_resources, args=(stop,), daemon=True).start()
    command = ["./task", *hosted_plan.invocation(targets)]
    if "arguments" in lane:
        command += ["--", *lane["arguments"]]
    if scope and name == "behavior":
        from ci_policy.impact import go_scope
        packages = go_scope(scope)
        if packages:
            command += ["--", *packages]
            record["packages"] = packages
    try:
        code = subprocess.call(command, cwd=ROOT, env=environment)
    finally:
        stop.set()
    record["oom_after"] = oom_events()
    record.update(finished_at=time.time(), exit_code=code,
                  status="passed" if code == 0 else "failed" if code == 1 else "unverified")
    record["classification"] = classify(record, verification_report.failures(artifact_root(ROOT)))
    path.write_text(json.dumps(record, indent=2) + "\n")
    timings.record(artifact_root(ROOT), record)
    return code

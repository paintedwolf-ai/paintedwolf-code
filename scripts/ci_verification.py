"""Hosted verification partitions, task invocation, and required-job verdicts."""

import argparse
from collections import Counter
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import threading
import time

from artifact_paths import artifact_root
from verification_plan import catalog, expand


ROOT = Path(__file__).resolve().parent.parent
PROFILES = {"check", "nightly", "release"}
SUITES = {"all", "behavior", "race", "coverage", "performance", "fuzz"}


def lanes():
    values = catalog()["ci"]
    taskfile = (ROOT / "Taskfile.yml").read_text()
    targets = set(re.findall(r"^  ([\w:-]+):$", taskfile, re.M))
    for name, lane in values.items():
        if not re.fullmatch(r"[a-z][a-z0-9-]*", name):
            raise ValueError(f"invalid CI lane: {name}")
        if set(lane) - {"targets", "minutes", "profiles", "suite", "native", "runner", "workers"}:
            raise ValueError(f"unknown CI lane fields: {name}")
        if not lane["targets"] or not set(lane["targets"]).issubset(targets):
            raise ValueError(f"CI lane {name} must name existing task targets")
        if type(lane["minutes"]) is not int or not 1 <= lane["minutes"] <= 300:
            raise ValueError(f"CI lane {name} must leave time for setup and evidence upload")
        if not lane["profiles"] or not set(lane["profiles"]).issubset(PROFILES):
            raise ValueError(f"invalid CI profiles: {name}")
        if lane["suite"] not in SUITES or type(lane["native"]) is not bool:
            raise ValueError(f"invalid CI setup or suite: {name}")
        if lane.get("runner", "macos-15") not in {"macos-15", "ubuntu-latest"}:
            raise ValueError(f"unsupported CI runner: {name}")
        if "workers" in lane and (type(lane["workers"]) is not int or not 1 <= lane["workers"] <= 8):
            raise ValueError(f"CI lane {name} workers must be an integer from 1 through 8")
    expected = Counter(stage["name"] for stage in expand(["check"]))
    actual = Counter(stage["name"] for lane in values.values() if "check" in lane["profiles"]
                     for stage in expand(lane["targets"]))
    if actual != expected:
        raise ValueError(f"CI check partition differs from check: missing={expected - actual}, extra={actual - expected}")
    return values


def matrix(profile, suite="all"):
    if profile not in PROFILES or suite not in SUITES or profile != "nightly" and suite != "all":
        raise ValueError(f"unsupported CI selection: {profile}/{suite}")
    result = []
    for name, lane in lanes().items():
        if profile not in lane["profiles"] or suite != "all" and lane["suite"] not in {"all", suite}:
            continue
        result.append({"lane": name, "minutes": lane["minutes"], "job_minutes": lane["minutes"] + 30,
                       "native": lane["native"],
                       "runner": lane.get("runner", "ubuntu-latest")})
    if not result:
        raise ValueError("CI selection contains no verification")
    return {"include": result}


def require_success(results):
    if not isinstance(results, dict) or not results:
        raise ValueError("required-job results are missing")
    failed = [f"{name}: {value.get('result', 'missing')}" for name, value in results.items()
              if value.get("result") != "success"]
    if failed:
        raise ValueError("required verification did not pass: " + ", ".join(failed))


def invocation(targets):
    """Expand private groups, which Task refuses to run by name, into their members."""
    plan = catalog()
    private = set(plan.get("private", []))
    return [member for target in targets
            for member in (plan["groups"][target] if target in private else [target])]


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


def run_lane(name):
    lane = lanes()[name]
    targets = lane["targets"]
    directory = artifact_root(ROOT) / "ci"
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / "run.json"
    record = {"lane": name, "targets": targets, "started_at": time.time(), "status": "running"}
    path.write_text(json.dumps(record, indent=2) + "\n")
    # Hosted runners are slower than development hosts; the lane budget, not the local default, bounds Go runs.
    environment = {"PW_GO_TEST_TIMEOUT_SECONDS": str(lane["minutes"] * 60), **os.environ}
    # A lane whose peak memory outgrows the runner caps its parallelism below the CPU count.
    if "workers" in lane:
        environment["PW_TEST_WORKERS"] = str(lane["workers"])
    # A hosted runner that runs out of memory or disk vanishes without evidence; the live log keeps these lines.
    stop = threading.Event()
    if os.environ.get("PW_TEST_HOST") == "dedicated" and Path("/proc/meminfo").exists():
        threading.Thread(target=sample_resources, args=(stop,), daemon=True).start()
    try:
        code = subprocess.call(["./task", *invocation(targets)], cwd=ROOT, env=environment)
    finally:
        stop.set()
    record.update(finished_at=time.time(), exit_code=code,
                  status="passed" if code == 0 else "failed" if code == 1 else "unverified")
    path.write_text(json.dumps(record, indent=2) + "\n")
    return code


def report(status):
    path = artifact_root(ROOT) / "ci" / "run.json"
    record = json.loads(path.read_text()) if path.exists() else {}
    lines = [f"### Verification: {status}", ""]
    if record:
        elapsed = (record.get("finished_at", time.time()) - record["started_at"]) / 60
        lines += [f"Targets: `{', '.join(record['targets'])}`", "", f"Elapsed: {elapsed:.1f} minutes.", ""]
        if record["status"] == "running":
            lines += ["The task invocation did not return a verdict; inspect the retained evidence.", ""]
    else:
        lines += ["No catalog task invocation was recorded; inspect the setup or custom job steps.", ""]
    lines += ["Receipts, stage logs, and available reports are retained with this job's artifacts.", ""]
    with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a") as output:
        output.write("\n".join(lines))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    plan = commands.add_parser("matrix")
    plan.add_argument("profile", choices=sorted(PROFILES))
    plan.add_argument("--suite", default="all", choices=sorted(SUITES))
    run = commands.add_parser("run")
    run.add_argument("lane")
    commands.add_parser("gate")
    summary = commands.add_parser("report")
    summary.add_argument("status")
    args = parser.parse_args()
    if args.command == "matrix":
        print(json.dumps(matrix(args.profile, args.suite), separators=(",", ":")))
    elif args.command == "run":
        return run_lane(args.lane)
    elif args.command == "gate":
        require_success(json.loads(os.environ["NEEDS_JSON"]))
    else:
        report(args.status)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

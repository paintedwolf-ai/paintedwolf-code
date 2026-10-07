"""Hosted verification partitions, task invocation, failure reports, and required-job verdicts."""

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
PROFILES = {"fast", "check", "nightly", "release"}
# Each profile on the left runs exactly the stages of the local gate on the right.
GATES = {"fast": "check-fast", "check": "check"}
# Runs of `CI/check` that executed the full tier; pull requests run the fast tier on a merge preview.
FULL_TIER_EVENTS = {"merge_group", "workflow_dispatch"}
# Output lines kept per failure in the job log and summary; the full logs travel with the evidence.
EXCERPT_LINES = 60
# Go's progress lines for tests that are running or passed; they bury a parallel package's failure.
GO_PROGRESS = re.compile(r"^=== (RUN|PAUSE|CONT|NAME)\b|^\s*--- (PASS|SKIP):")
SUITES = {"all", "behavior", "race", "coverage", "performance", "fuzz"}


def lanes():
    values = catalog()["ci"]
    taskfile = (ROOT / "Taskfile.yml").read_text()
    targets = set(re.findall(r"^  ([\w:-]+):$", taskfile, re.M))
    for name, lane in values.items():
        if not re.fullmatch(r"[a-z][a-z0-9-]*", name):
            raise ValueError(f"invalid CI lane: {name}")
        if set(lane) - {"targets", "minutes", "profiles", "suite", "native", "runner", "workers", "shards"}:
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
        if "shards" in lane and (type(lane["shards"]) is not int or not 2 <= lane["shards"] <= 8):
            raise ValueError(f"CI lane {name} shards must be an integer from 2 through 8")
    for profile, gate in GATES.items():
        expected = Counter(stage["name"] for stage in expand([gate]))
        actual = Counter(stage["name"] for lane in values.values() if profile in lane["profiles"]
                         for stage in expand(lane["targets"]))
        if actual != expected:
            raise ValueError(f"CI {profile} partition differs from {gate}: "
                             f"missing={expected - actual}, extra={actual - expected}")
    return values


def matrix(profile, suite="all"):
    if profile not in PROFILES or suite not in SUITES or profile != "nightly" and suite != "all":
        raise ValueError(f"unsupported CI selection: {profile}/{suite}")
    result = []
    for name, lane in lanes().items():
        if profile not in lane["profiles"] or suite != "all" and lane["suite"] not in {"all", suite}:
            continue
        count = lane.get("shards", 1)
        for index in range(1, count + 1):
            result.append({"lane": name, "shard": f"{index}/{count}" if count > 1 else "",
                           "minutes": lane["minutes"], "job_minutes": lane["minutes"] + 30,
                           "native": lane["native"],
                           "runner": lane.get("runner", "ubuntu-latest")})
    if not result:
        raise ValueError("CI selection contains no verification")
    return {"include": result}


def require_success(results, skipped=()):
    """Every job passed, except those the caller's tier skips on purpose, which must not have run."""
    if not isinstance(results, dict) or not results:
        raise ValueError("required-job results are missing")
    unknown = set(skipped) - set(results)
    if unknown:
        raise ValueError("jobs expected to be skipped are not required: " + ", ".join(sorted(unknown)))
    failed = [f"{name}: {value.get('result', 'missing')}" for name, value in results.items()
              if value.get("result") != ("skipped" if name in skipped else "success")]
    if failed:
        raise ValueError("required verification did not pass: " + ", ".join(failed))


def github(path, **query):
    command = ["gh", "api", "--method", "GET", path, *(f"-f{key}={value}" for key, value in query.items())]
    return json.loads(subprocess.run(command, check=True, capture_output=True, text=True).stdout)


def require_full_tier(repository, sha):
    """The commit itself passed `CI/check` in the full tier, as every commit the merge queue lands has."""
    runs = github(f"repos/{repository}/commits/{sha}/check-runs", check_name="check", filter="all")["check_runs"]
    events = set()
    for run in runs:
        if run["app"]["slug"] != "github-actions" or run["conclusion"] != "success":
            continue
        suite = run["check_suite"]["id"]
        events |= {item["event"] for item in github(f"repos/{repository}/actions/runs", check_suite_id=suite)["workflow_runs"]}
    if not events & FULL_TIER_EVENTS:
        raise ValueError(f"{sha} has no passing full-tier CI/check; land it through the merge queue "
                         "or dispatch CI on it before releasing")


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


def run_lane(name, shard=""):
    lane = lanes()[name]
    count = lane.get("shards", 1)
    if (shard == "") != (count == 1) or shard and shard not in {f"{k}/{count}" for k in range(1, count + 1)}:
        raise ValueError(f"CI lane {name} has {count} shard(s); got {shard!r}")
    targets = lane["targets"]
    directory = artifact_root(ROOT) / "ci"
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / "run.json"
    record = {"lane": name, "shard": shard, "targets": targets, "started_at": time.time(), "status": "running"}
    path.write_text(json.dumps(record, indent=2) + "\n")
    # Hosted runners are slower than development hosts; the lane budget, not the local default, bounds Go runs.
    environment = {"PW_GO_TEST_TIMEOUT_SECONDS": str(lane["minutes"] * 60), **os.environ}
    # A lane whose peak memory outgrows the runner caps its parallelism below the CPU count.
    if "workers" in lane:
        environment["PW_TEST_WORKERS"] = str(lane["workers"])
    # Each shard of a split lane runs its slice of every Go stage's packages.
    if shard:
        environment["PW_GO_SHARD"] = shard
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


def failures(root):
    """Stages and packages this job's receipts did not pass, with the tests a digest named."""
    found = []
    for receipt in sorted((root / "verification").glob("*/[0-9]*.json")):
        if receipt.name.endswith(".progress.json"):
            continue
        record = json.loads(receipt.read_text())
        if record.get("status") == "passed":
            continue
        stages = [{"stage": evidence["stage"], "subject": subject, "status": result.get("status"),
                   "tests": result.get("tests", []), "output": result.get("output"), "log": result.get("log")}
                  for evidence in record.get("evidence", [])
                  for subject, result in evidence.get("results", {}).items() if result.get("status") != "passed"]
        # A run stopped before any stage reported, such as a cancellation, has only the receipt's reason.
        found += stages or [{"stage": ", ".join(record.get("names", [])), "subject": "", "status": record.get("status"),
                             "tests": [], "output": None, "log": None, "reason": record.get("reason")}]
    return found


def excerpt(failure):
    """The digest's failure output, or the end of the stage log when no digest isolated one."""
    for key, tail in (("output", False), ("log", True)):
        path = failure.get(key)
        if path and Path(path).is_file():
            lines = [line for line in Path(path).read_text(errors="replace").splitlines() if not GO_PROGRESS.match(line)]
            return lines[-EXCERPT_LINES:] if tail else lines[:EXCERPT_LINES]
    return []


def named_subject(failure):
    """The failing package, or nothing for a stage that reports as a single task."""
    return failure["subject"] if failure["subject"] not in {"", "task"} else ""


def annotation(failure):
    """A workflow error the pull request's checks show without opening the log."""
    message = ": ".join(part for part in (named_subject(failure), ", ".join(failure["tests"])) if part)
    message = message or failure.get("reason") or "see the job summary"
    return f"::error title={failure['stage']} {failure['status']}::{message}".replace("\n", "%0A")


def summary_lines(failure):
    subject = f" · `{named_subject(failure)}`" if named_subject(failure) else ""
    tests = ", ".join(f"`{test}`" for test in failure["tests"])
    reason = f" ({failure['reason']})" if failure.get("reason") else ""
    heading = f"- **{failure['status']}** `{failure['stage']}`{subject}" + (f": {tests}" if tests else "") + reason
    body = excerpt(failure)
    if not body:
        return [heading]
    return [heading, "  <details><summary>Output</summary>", "", "  ```text",
            *(f"  {line}" for line in body), "  ```", "  </details>"]


def report(status):
    root = artifact_root(ROOT)
    path = root / "ci" / "run.json"
    record = json.loads(path.read_text()) if path.exists() else {}
    lines = [f"### Verification: {status}", ""]
    if record:
        elapsed = (record.get("finished_at", time.time()) - record["started_at"]) / 60
        lines += [f"Targets: `{', '.join(record['targets'])}`", "", f"Elapsed: {elapsed:.1f} minutes.", ""]
        if record["status"] == "running":
            lines += ["The task invocation did not return a verdict; inspect the retained evidence.", ""]
    else:
        lines += ["No catalog task invocation was recorded; inspect the setup or custom job steps.", ""]
    found = failures(root)
    if found:
        lines += ["#### Not passed", ""]
        for failure in found:
            lines += summary_lines(failure)
            print("\n".join([annotation(failure), *excerpt(failure), ""]), flush=True)
        lines.append("")
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
    run.add_argument("--shard", default="")
    gate = commands.add_parser("gate")
    gate.add_argument("--skipped", type=lambda value: [name for name in value.split(",") if name], default=[],
                      help="comma-separated jobs this tier skips on purpose")
    summary = commands.add_parser("report")
    summary.add_argument("status")
    verified = commands.add_parser("verified")
    verified.add_argument("sha")
    args = parser.parse_args()
    if args.command == "matrix":
        print(json.dumps(matrix(args.profile, args.suite), separators=(",", ":")))
    elif args.command == "run":
        return run_lane(args.lane, args.shard)
    elif args.command == "gate":
        require_success(json.loads(os.environ["NEEDS_JSON"]), args.skipped)
    elif args.command == "verified":
        require_full_tier(os.environ["GITHUB_REPOSITORY"], args.sha)
    else:
        report(args.status)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

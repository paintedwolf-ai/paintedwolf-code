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
from ci_policy.evidence import oom_events, classify, failure_signature
from verification_plan import catalog, expand


ROOT = Path(__file__).resolve().parent.parent
PROFILES = {"fast", "check", "nightly", "release", "integration"}
# Each profile on the left runs exactly the stages of the local gate on the right.
GATES = {"fast": "check-fast", "check": "check"}
# Runs of `CI/check` that executed the full tier; pull requests run the fast tier on a merge preview.
FULL_TIER_EVENTS = {"merge_group", "workflow_dispatch"}
# Run states that still hold, or will claim, a runner.
UNFINISHED_RUNS = ("requested", "waiting", "pending", "queued", "in_progress")
QUEUE_BRANCHES = "gh-readonly-queue/"
# Output lines kept per failure in the job log and summary; the full logs travel with the evidence.
EXCERPT_LINES = 60
# Go's progress lines for tests that are running or passed; they bury a parallel package's failure.
GO_PROGRESS = re.compile(r"^=== (RUN|PAUSE|CONT|NAME)\b|^\s*--- (PASS|SKIP):")
SUITES = {"all", "behavior", "race", "coverage", "performance", "fuzz", "e2e"}
# What a lane installs: the shared toolchains, plus the Tauri shell and its staged engine,
# or the Den Rust workspace and harness stack without the shell's packaging inputs.
SETUPS = {"verification", "shell", "harness"}


def lanes():
    from ci_policy.quarantine import entries
    entries()
    values = catalog()["ci"]
    taskfile = (ROOT / "Taskfile.yml").read_text()
    targets = set(re.findall(r"^  ([\w:-]+):$", taskfile, re.M))
    for name, lane in values.items():
        if not re.fullmatch(r"[a-z][a-z0-9-]*", name):
            raise ValueError(f"invalid CI lane: {name}")
        if set(lane) - {"targets", "minutes", "profiles", "suite", "setup", "runner", "workers", "shards"}:
            raise ValueError(f"unknown CI lane fields: {name}")
        if not lane["targets"] or not set(lane["targets"]).issubset(targets):
            raise ValueError(f"CI lane {name} must name existing task targets")
        if type(lane["minutes"]) is not int or not 1 <= lane["minutes"] <= 300:
            raise ValueError(f"CI lane {name} must leave time for setup and evidence upload")
        if not lane["profiles"] or not set(lane["profiles"]).issubset(PROFILES):
            raise ValueError(f"invalid CI profiles: {name}")
        if lane["suite"] not in SUITES or lane.get("setup") not in SETUPS:
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
    expected = Counter(stage["name"] for stage in expand(["check"]) if stage["name"] != "build:cross")
    actual = Counter(stage["name"] for lane in values.values() if "integration" in lane["profiles"]
                     for stage in expand(lane["targets"]))
    if actual != expected:
        raise ValueError(f"integration partition differs from check minus cross compilation: {expected - actual}, {actual - expected}")
    return values


def analysis_set(targets):
    lint = bool(set(targets) & {"lint:fast", "lint:full"})
    vulnerabilities = bool(set(targets) & {"lint:vuln", "lint:vuln:fresh"})
    return "all" if lint and vulnerabilities else "lint" if lint else "vulnerabilities" if vulnerabilities else "none"


def matrix(profile, suite="all", scope=None):
    if profile not in PROFILES or suite not in SUITES or profile != "nightly" and suite != "all":
        raise ValueError(f"unsupported CI selection: {profile}/{suite}")
    if profile == "integration" and scope is not None and scope["full"]:
        profile = "check"
    result = []
    values = lanes()
    selected = set(values)
    if profile == "integration" and scope is not None:
        from ci_policy.impact import selected_lanes
        selected = selected_lanes(scope, values)
    for name, lane in values.items():
        if name not in selected:
            continue
        if profile not in lane["profiles"] or suite != "all" and lane["suite"] not in {"all", suite}:
            continue
        count = lane.get("shards", 1)
        for index in range(1, count + 1):
            result.append({"lane": name, "shard": f"{index}/{count}" if count > 1 else "",
                           "minutes": lane["minutes"], "job_minutes": lane["minutes"] + 30,
                           "setup": lane["setup"],
                           "notices": "licenses:notices" in lane["targets"],
                           "analysis": analysis_set(lane["targets"]),
                           "runner": lane.get("runner", "ubuntu-latest")})
    if not result:
        raise ValueError("CI selection contains no verification")
    return {"include": result}


def require_success(results, skipped=(), draft=False):
    """Every job passed, except those the caller's tier skips on purpose, which must not have run."""
    if draft:
        raise ValueError("draft pull requests are not verified; mark the pull request ready for review to run its checks")
    if not isinstance(results, dict) or not results:
        raise ValueError("required-job results are missing")
    unknown = set(skipped) - set(results)
    if unknown:
        raise ValueError("jobs expected to be skipped are not required: " + ", ".join(sorted(unknown)))
    failed = [f"{name}: {value.get('result', 'missing')}" for name, value in results.items()
              if value.get("result") != ("skipped" if name in skipped else "success")]
    if failed:
        raise ValueError("required verification did not pass: " + ", ".join(failed))


def github(path, method="GET", **query):
    command = ["gh", "api", "--method", method, path, *(f"-f{key}={value}" for key, value in query.items())]
    output = subprocess.run(command, check=True, capture_output=True, text=True).stdout
    return json.loads(output) if output.strip() else None


def prune_merge_queue(repository):
    """Cancel CI runs for merge groups the queue already merged, rebuilt, or dropped.

    The queue deletes a group's branch when the group ends but leaves its runs holding runners.
    Runs are listed before branches, so a group created in between counts as live.
    """
    runs = [run for status in UNFINISHED_RUNS
            for run in github(f"repos/{repository}/actions/workflows/ci.yml/runs",
                              event="merge_group", status=status, per_page=100)["workflow_runs"]]
    live = {ref["ref"].removeprefix("refs/heads/")
            for ref in github(f"repos/{repository}/git/matching-refs/heads/{QUEUE_BRANCHES}")}
    stale = [run for run in runs if run["head_branch"] not in live]
    for run in stale:
        # A plain cancel still schedules a stale run's always() steps; force-cancel stops it outright.
        github(f"repos/{repository}/actions/runs/{run['id']}/force-cancel", method="POST")
        print(f"cancelled run {run['id']}: merge group {run['head_branch']} no longer exists", flush=True)
    return [run["id"] for run in stale]


def task_json(arguments):
    """Run a queue control through ./task and parse the JSON it prints after Task's command echo."""
    result = subprocess.run(["./task", *arguments], cwd=ROOT, capture_output=True, text=True)
    output = result.stdout[result.stdout.find("{"):] if "{" in result.stdout else ""
    return result.returncode, json.loads(output) if output else None


def release_leftover_requests(step):
    """Withdraw requests an earlier step left in the queue so cleanup is not admitted behind them.

    A shared batch outlives the client that submitted it, so a step killed at its deadline leaves
    its run holding admission. A hosted runner serves one job, so every request it holds is this job's.
    """
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


def require_full_tier(repository, sha):
    """Release eligibility comes from qualification of this exact commit."""
    runs = github(f"repos/{repository}/actions/workflows/qualification.yml/runs",
                  head_sha=sha, per_page=100)["workflow_runs"]
    if not any(run["head_sha"] == sha and run["head_branch"] == "main"
               and run["event"] in {"push", "workflow_dispatch"}
               and run["conclusion"] == "success" for run in runs):
        raise ValueError(f"{sha} has no passing exact-commit qualification; qualify it before releasing")


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
    scope = None
    if os.environ.get("PW_CI_AFFECTED") == "1":
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
    # Each shard of a split lane runs its slice of every Go stage's packages.
    if shard:
        environment["PW_GO_SHARD"] = shard
    # A hosted runner that runs out of memory or disk vanishes without evidence; the live log keeps these lines.
    stop = threading.Event()
    if os.environ.get("PW_TEST_HOST") == "dedicated" and Path("/proc/meminfo").exists():
        threading.Thread(target=sample_resources, args=(stop,), daemon=True).start()
    command = ["./task", *invocation(targets)]
    if scope and name == "behavior":
        from ci_policy.impact import go_scope
        packages = go_scope(scope)
        if packages:
            # Deal after selection, so a small closure cannot produce an empty shard.
            if shard:
                index, total = map(int, shard.split("/"))
                packages = packages[(index - 1) % len(packages)::min(total, len(packages))]
                environment.pop("PW_GO_SHARD", None)
            command += ["--", *packages]
            record["packages"] = packages
    try:
        code = subprocess.call(command, cwd=ROOT, env=environment)
    finally:
        stop.set()
    record["oom_after"] = oom_events()
    record.update(finished_at=time.time(), exit_code=code,
                  status="passed" if code == 0 else "failed" if code == 1 else "unverified")
    record["classification"] = classify(record, failures(artifact_root(ROOT)))
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
                   "tests": result.get("tests", []), "output": result.get("output"), "log": result.get("log"), "resource_limit": result.get("resource_limit")}
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
    found = failures(root)
    for failure in found:
        failure["signature"] = failure_signature(failure)
    (root / "ci").mkdir(parents=True, exist_ok=True)
    (root / "ci" / "failures.json").write_text(json.dumps(found, indent=2) + "\n")
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
    plan.add_argument("--affected", action="store_true")
    plan.add_argument("--suite", default="all", choices=sorted(SUITES))
    run = commands.add_parser("run")
    run.add_argument("lane")
    run.add_argument("--shard", default="")
    gate = commands.add_parser("gate")
    gate.add_argument("--skipped", type=lambda value: [name for name in value.split(",") if name], default=[],
                      help="comma-separated jobs this tier skips on purpose")
    gate.add_argument("--draft", choices=["true", "false"], default="false",
                      help="whether the run verifies a draft pull request")
    summary = commands.add_parser("report")
    summary.add_argument("status")
    verified = commands.add_parser("verified")
    verified.add_argument("sha")
    commands.add_parser("prune")
    release = commands.add_parser("release")
    release.add_argument("step", help="the step whose leftover requests are withdrawn")
    args = parser.parse_args()
    if args.command == "matrix":
        from ci_policy.impact import change
        scope = change() if args.affected else None
        if scope and os.environ.get("GITHUB_STEP_SUMMARY"):
            with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a") as summary:
                summary.write("### Integration scope\n\n```json\n" + json.dumps(scope, indent=2) + "\n```\n")
        print(json.dumps(matrix(args.profile, args.suite, scope), separators=(",", ":")))
    elif args.command == "run":
        return run_lane(args.lane, args.shard)
    elif args.command == "gate":
        require_success(json.loads(os.environ["NEEDS_JSON"]), args.skipped, args.draft == "true")
    elif args.command == "verified":
        require_full_tier(os.environ["GITHUB_REPOSITORY"], args.sha)
    elif args.command == "prune":
        prune_merge_queue(os.environ["GITHUB_REPOSITORY"])
    elif args.command == "release":
        # Cleanup still runs; a request that would not release is reported, not fatal.
        unreleased = release_leftover_requests(args.step)
        if unreleased:
            print(f"::warning::queue requests still held after cancellation: {', '.join(unreleased)}", flush=True)
    else:
        report(args.status)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

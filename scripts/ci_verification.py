"""Hosted verification partitions, task invocation, and required-job verdicts."""

import argparse
from collections import Counter
import json
import os
from pathlib import Path
import re
import subprocess
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
        if set(lane) - {"targets", "minutes", "profiles", "suite", "native", "runner"}:
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
    expected = Counter(stage["name"] for stage in expand(["check"]))
    actual = Counter(stage["name"] for lane in values.values() if "check" in lane["profiles"]
                     for stage in expand(lane["targets"]))
    if actual != expected:
        raise ValueError(f"CI check partition differs from check: missing={expected - actual}, extra={actual - expected}")
    if any("release" not in lane["profiles"] for lane in values.values() if "check" in lane["profiles"]):
        raise ValueError("release verification must include every check partition")
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
                       "runner": lane.get("runner", "ubuntu-latest" if profile == "check" else "macos-15")})
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


def run_lane(name):
    targets = lanes()[name]["targets"]
    directory = artifact_root(ROOT) / "ci"
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / "run.json"
    record = {"lane": name, "targets": targets, "started_at": time.time(), "status": "running"}
    path.write_text(json.dumps(record, indent=2) + "\n")
    code = subprocess.call(["./task", *invocation(targets)], cwd=ROOT)
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

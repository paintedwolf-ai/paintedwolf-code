"""Lane and package durations that main's nightly measures and later hosted plans read."""
import json
import math
from pathlib import Path
import subprocess
import sys
import tempfile

from ci_policy import github

WORKFLOW = "nightly.yml"
RECEIPTS = ("receipt-verification-nightly-",)
RECENT_RUNS = 20


def row_key(lane, shard):
    return f"{lane} {shard}" if shard else lane


def valid_seconds(value, *, allow_zero=False):
    return type(value) in (int, float) and math.isfinite(value) and (value >= 0 if allow_zero else value > 0)


def record(root, run):
    """Write the lane's elapsed time beside its receipts; a sharded lane adds its Go packages' durations."""
    packages = {}
    if run["shard"]:
        for events in sorted((root / "verification").glob("*/stage-*-events.jsonl")):
            for line in events.read_text().splitlines():
                event = json.loads(line)
                if isinstance(event.get("package"), str) and type(event.get("elapsed")) in (int, float) \
                        and not event.get("reused"):
                    packages[event["package"]] = packages.get(event["package"], 0) + event["elapsed"]
    job_start = run.get('job_started_at')
    complete = valid_seconds(job_start, allow_zero=True) and job_start <= run['started_at']
    start = job_start if complete else run['started_at']
    measured = {"basis": "setup-and-execution" if complete else "execution", "lane": run["lane"], "shard": run["shard"], "status": run["status"],
                "seconds": round(run["finished_at"] - start, 1),
                "packages": {name: round(seconds, 3) for name, seconds in sorted(packages.items())}}
    (root / "ci" / "timings.json").write_text(json.dumps(measured, indent=2) + "\n")


def merge(paths):
    """Passed lanes' durations by matrix row, and every package duration the sharded lanes measured."""
    lanes, packages = {}, {}
    for path in paths:
        measured = json.loads(path.read_text())
        if (measured.get("basis") == "setup-and-execution" and measured["status"] == "passed"
                and valid_seconds(measured["seconds"])):
            lanes[row_key(measured["lane"], measured["shard"])] = measured["seconds"]
        packages.update({name: seconds for name, seconds in measured["packages"].items()
                         if valid_seconds(seconds, allow_zero=True)})
    return {"lanes": lanes, "packages": packages}


def download(repository, run, names, directory):
    command = ["gh", "run", "download", str(run), "--repo", repository, "--dir", str(directory)]
    for name in names:
        command += ["--name", name]
    subprocess.run(command, check=True, capture_output=True, text=True)


def nightly_rows():
    from verification_plan import catalog
    return {row_key(name, f"{index}/{count}" if count > 1 else "")
            for name, lane in catalog()["ci"].items()
            if "nightly" in lane["profiles"]
            for count in [lane.get("shards", 1)] for index in range(1, count + 1)}


def latest(repository):
    """Newest measurements per row, filling incomplete nightly runs from bounded main history."""
    path = f"repos/{repository}/actions/workflows/{WORKFLOW}/runs?branch=main&status=completed&per_page={RECENT_RUNS}"
    result = {"runs": [], "lanes": {}, "packages": {}, "lane_runs": {}}
    required = nightly_rows()
    for run in github.api(path)["workflow_runs"]:
        names = [artifact["name"] for artifact in github.pages(f"repos/{repository}/actions/runs/{run['id']}/artifacts",
                                                                "artifacts")
                 if artifact["name"].startswith(RECEIPTS) and not artifact["expired"]]
        if not names:
            continue
        with tempfile.TemporaryDirectory() as directory:
            download(repository, run["id"], names, directory)
            measured = merge(sorted(Path(directory).glob("*/**/ci/timings.json")))
        contributed = False
        for key, seconds in measured["lanes"].items():
            if key not in result["lanes"]:
                result["lanes"][key] = seconds
                result["lane_runs"][key] = run["id"]
                contributed = True
        for key, seconds in measured["packages"].items():
            if key not in result["packages"]:
                result["packages"][key] = seconds
                contributed = True
        if contributed:
            result["runs"].append(run["id"])
        if required <= result["lanes"].keys():
            break
    return result if result["lanes"] else {}


def resolve(repository):
    """Measured timings, or none: a plan without them falls back to lane budgets and even shards."""
    try:
        return latest(repository)
    except (subprocess.CalledProcessError, OSError, KeyError, ValueError) as error:
        detail = error.stderr.strip() if isinstance(error, subprocess.CalledProcessError) else error
        print(f"::warning title=Measured timings unavailable::{detail}".replace("\n", "%0A"), file=sys.stderr)
        return {}

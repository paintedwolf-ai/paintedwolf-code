"""Lane and package durations that main's qualification measures and later hosted plans read."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile

from ci_policy import github

WORKFLOW = "qualification.yml"
RECEIPTS = "receipt-verification-check-"
RECENT_RUNS = 10


def row_key(lane, shard):
    return f"{lane} {shard}" if shard else lane


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
    measured = {"lane": run["lane"], "shard": run["shard"], "status": run["status"],
                "seconds": round(run["finished_at"] - run["started_at"], 1),
                "packages": {name: round(seconds, 3) for name, seconds in sorted(packages.items())}}
    (root / "ci" / "timings.json").write_text(json.dumps(measured, indent=2) + "\n")


def merge(paths):
    """Passed lanes' durations by matrix row, and every package duration the sharded lanes measured."""
    lanes, packages = {}, {}
    for path in paths:
        measured = json.loads(path.read_text())
        if measured["status"] == "passed":
            lanes[row_key(measured["lane"], measured["shard"])] = measured["seconds"]
        packages.update(measured["packages"])
    return {"lanes": lanes, "packages": packages}


def download(repository, run, names, directory):
    command = ["gh", "run", "download", str(run), "--repo", repository, "--dir", str(directory)]
    for name in names:
        command += ["--name", name]
    subprocess.run(command, check=True, capture_output=True, text=True)


def latest(repository):
    """Timings from the newest completed qualification on main whose lanes recorded them."""
    path = f"repos/{repository}/actions/workflows/{WORKFLOW}/runs?branch=main&status=completed&per_page={RECENT_RUNS}"
    for run in github.api(path)["workflow_runs"]:
        names = [artifact["name"] for artifact in github.pages(f"repos/{repository}/actions/runs/{run['id']}/artifacts",
                                                                "artifacts")
                 if artifact["name"].startswith(RECEIPTS) and not artifact["expired"]]
        if not names:
            continue
        with tempfile.TemporaryDirectory() as directory:
            download(repository, run["id"], names, directory)
            measured = merge(sorted(Path(directory).glob("*/**/ci/timings.json")))
        if measured["lanes"]:
            return {"run": run["id"], **measured}
    return {}


def resolve(repository):
    """Measured timings, or none: a plan without them falls back to lane budgets and even shards."""
    try:
        return latest(repository)
    except (subprocess.CalledProcessError, OSError, KeyError, ValueError) as error:
        detail = error.stderr.strip() if isinstance(error, subprocess.CalledProcessError) else error
        print(f"::warning title=Measured timings unavailable::{detail}".replace("\n", "%0A"), file=sys.stderr)
        return {}

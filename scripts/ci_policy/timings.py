"""Durations nightly measures: each lane records its own, the run publishes them merged in one
artifact, and every later hosted plan reads that file."""
import io
import json
import math
import os
from pathlib import Path
import subprocess
import sys
import zipfile

from ci_policy import github

ARTIFACT = "nightly-timings"
FILE = "timings.json"
SPEC_FILES = "e2e-specs-*.json"
MAX_BYTES = 4 * 1024 * 1024
SLOWEST_TESTS = 50


def package_lanes():
    """Lanes that run the Go packages a change affects, so they measure each package."""
    from verification_plan import catalog
    return {name for name, lane in catalog()["ci"].items() if lane.get("phases") == "affected-packages"}


def row_key(lane, shard):
    return f"{lane} {shard}" if shard else lane


def valid_seconds(value, *, allow_zero=False):
    return type(value) in (int, float) and math.isfinite(value) and (value >= 0 if allow_zero else value > 0)


def slowest(tests):
    """The longest tests first; ties order by name so the file is stable."""
    ranked = sorted(tests, key=lambda row: (-row["seconds"], row["package"], row["test"]))
    return ranked[:SLOWEST_TESTS]


def test_durations(root):
    """Every top-level Go test that finished in this job's stage streams."""
    tests = {}
    for stream in sorted((root / "verification").glob("*/stage-*-go.json")):
        with stream.open(errors="replace") as lines:
            for line in lines:
                try:
                    event = json.loads(line)
                except ValueError:
                    continue
                if not isinstance(event, dict):
                    continue
                name, package = event.get("Test"), event.get("Package")
                if (event.get("Action") in ("pass", "fail") and isinstance(name, str) and "/" not in name
                        and isinstance(package, str) and valid_seconds(event.get("Elapsed"))):
                    key = (package, name)
                    tests[key] = max(tests.get(key, 0), event["Elapsed"])
    return [{"package": package, "test": name, "seconds": seconds} for (package, name), seconds in tests.items()]


def record(root, run):
    """Write the lane's elapsed time and slowest tests beside its receipts; a sharded lane adds its
    Go packages' durations."""
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
                # A signal tears the job down part-way, so its time measures only where it stopped.
                "finished": type(run.get("exit_code")) is int and run["exit_code"] >= 0,
                "seconds": round(run["finished_at"] - start, 1),
                "packages": {name: round(seconds, 3) for name, seconds in sorted(packages.items())},
                "tests": slowest(test_durations(root))}
    (root / "ci" / "timings.json").write_text(json.dumps(measured, indent=2) + "\n")


def spec_durations(report):
    """Seconds each Playwright spec file ran, retries included, from the JSON reporter's output."""
    def milliseconds(suite):
        own = sum(result["duration"] for spec in suite.get("specs", []) for test in spec.get("tests", [])
                  for result in test.get("results", []) if valid_seconds(result.get("duration"), allow_zero=True))
        return own + sum(milliseconds(child) for child in suite.get("suites", []))
    durations = {}
    for suite in report.get("suites", []):
        elapsed = milliseconds(suite)
        if isinstance(suite.get("file"), str) and elapsed > 0:
            durations[suite["file"]] = round(durations.get(suite["file"], 0) + elapsed / 1000, 3)
    return durations


def record_specs(report, output):
    """Write one browser shard's spec durations beside the job's receipts."""
    output = Path(output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(spec_durations(json.loads(Path(report).read_text())), indent=2, sort_keys=True) + "\n")


def merge_specs(paths):
    """Browser spec durations by file; a later attempt replaces the run it reran."""
    specs = {}
    for path in paths:
        measured = json.loads(path.read_text())
        if isinstance(measured, dict):
            specs.update({name: seconds for name, seconds in measured.items() if valid_seconds(seconds)})
    return specs


def merge(paths):
    """Durations of lanes that finished, whatever their verdict, by matrix row; every package duration the
    uninstrumented lane measured; and the slowest tests across lanes."""
    lanes, packages, tests = {}, {}, {}
    for path in paths:
        measured = json.loads(path.read_text())
        if (measured.get("basis") == "setup-and-execution" and measured.get("finished") is True
                and valid_seconds(measured["seconds"])):
            lanes[row_key(measured["lane"], measured["shard"])] = measured["seconds"]
        if measured.get("lane") in package_lanes():
            packages.update({name: seconds for name, seconds in measured["packages"].items()
                             if valid_seconds(seconds, allow_zero=True)})
        for row in measured.get("tests", []):
            if valid_seconds(row.get("seconds")) and isinstance(row.get("package"), str) and isinstance(row.get("test"), str):
                key = (row["package"], row["test"])
                tests[key] = max(tests.get(key, 0), row["seconds"])
    return {"lanes": lanes, "packages": packages,
            "tests": slowest([{"package": package, "test": name, "seconds": seconds}
                              for (package, name), seconds in tests.items()])}


def publish(receipts, output, environment):
    """One file for the whole run, bound to the source it measured."""
    # One directory per receipt; the archive may root it at `ci/`. A later attempt sorts after the one it reran.
    measured = merge(sorted(Path(receipts).glob("*/**/timings.json")))
    measured["specs"] = merge_specs(sorted(Path(receipts).glob(f"*/**/{SPEC_FILES}")))
    measured.update(run=int(environment["GITHUB_RUN_ID"]),
                    source_sha=environment["SOURCE_SHA"], source_tree=environment["SOURCE_TREE"])
    Path(output).write_text(json.dumps(measured, indent=2, sort_keys=True) + "\n")
    return measured


def decode(raw):
    if len(raw) > MAX_BYTES:
        raise ValueError("timings archive exceeds its limit")
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        members = archive.infolist()
        if len(members) != 1 or members[0].filename != FILE or members[0].file_size > MAX_BYTES:
            raise ValueError("timings archive has unexpected contents")
        measured = json.loads(archive.read(members[0]))
    if not isinstance(measured, dict) or not isinstance(measured.get("lanes"), dict) \
            or not isinstance(measured.get("packages"), dict):
        raise ValueError("timings file has no lane and package durations")
    return measured


def latest(repository):
    """The newest unexpired timings file a main nightly published; one listing and one download."""
    listing = github.api(f"repos/{repository}/actions/artifacts?name={ARTIFACT}&per_page=100")
    published = [artifact for artifact in listing["artifacts"]
                 if not artifact["expired"] and artifact["workflow_run"]["head_branch"] == "main"]
    if not published:
        print(f"No main nightly has published {ARTIFACT}", file=sys.stderr)
        return {}
    artifact = max(published, key=lambda row: row["created_at"])
    print(f"Downloading {ARTIFACT} artifact {artifact['id']} from nightly run {artifact['workflow_run']['id']}",
          file=sys.stderr)
    return decode(subprocess.check_output(["gh", "api", f"repos/{repository}/actions/artifacts/{artifact['id']}/zip"]))


def resolve(repository):
    """Measured timings, or none: a plan without them falls back to lane budgets and even shards."""
    try:
        return latest(repository)
    except (subprocess.CalledProcessError, OSError, KeyError, TypeError, ValueError, zipfile.BadZipFile) as error:
        detail = error.stderr.strip() if isinstance(error, subprocess.CalledProcessError) and isinstance(error.stderr, str) else error
        print(f"::warning title=Measured timings unavailable::{detail}".replace("\n", "%0A"), file=sys.stderr)
        return {}


def main(arguments):
    if arguments[0] == "specs":
        record_specs(*arguments[1:])
        return 0
    receipts, output = arguments
    measured = publish(receipts, output, os.environ)
    print(f"Merged {len(measured['lanes'])} lane, {len(measured['packages'])} package, "
          f"{len(measured['tests'])} test and {len(measured['specs'])} browser spec durations from {measured['source_sha']}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))

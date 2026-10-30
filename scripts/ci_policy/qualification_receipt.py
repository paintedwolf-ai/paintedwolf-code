"""Bind completed qualification to its source, controller, and workflow attempt."""
import io
import json
import os
from pathlib import Path
import re
import subprocess
import zipfile

from ci_policy.github import pages

CHECKS = {"source", "verification", "platform", "platform-lanes", "e2e"}
MAX_BYTES = 64 * 1024
IDENTITY = {"version", "repository", "event", "run_id", "run_attempt", "source_sha", "source_tree", "controller_sha"}


def valid_identity(record):
    return (isinstance(record, dict) and type(record.get("version")) is int and record["version"] == 1
            and isinstance(record.get("repository"), str)
            and record.get("event") == "workflow_dispatch"
            and all(type(record.get(key)) is int and record[key] > 0 for key in ("run_id", "run_attempt"))
            and all(isinstance(record.get(key), str) and re.fullmatch(r"[0-9a-f]{40}", record[key])
                    for key in ("source_sha", "source_tree", "controller_sha")))


def complete(record, results, environment):
    from ci_policy.verification_verdict import require_success
    if not valid_identity(record) or set(record) != IDENTITY:
        raise ValueError("qualification source binding is missing or invalid")
    attempt = int(environment["GITHUB_RUN_ATTEMPT"])
    expected = {"repository": environment["GITHUB_REPOSITORY"], "run_id": int(environment["GITHUB_RUN_ID"]),
                "controller_sha": environment["GITHUB_WORKFLOW_SHA"],
                "event": environment["GITHUB_EVENT_NAME"]}
    # Failed-job reruns retain successful source outputs from an earlier attempt.
    if record["run_attempt"] > attempt or any(record.get(key) != value for key, value in expected.items()):
        raise ValueError("qualification source binding belongs to another workflow attempt")
    if not isinstance(results, dict) or set(results) != CHECKS:
        raise ValueError("qualification requires the complete declared stage set")
    require_success(results)
    return record | {
        "run_attempt": attempt, "checks": {name: results[name]["result"] for name in sorted(CHECKS)}}


def matches(record, repository, source, run):
    return (valid_identity(record) and set(record) == IDENTITY | {"checks"} and record.get("repository") == repository and record.get("source_sha") == source
            and record.get("controller_sha") == run.get("head_sha") and record.get("run_id") == run.get("id")
            and record.get("run_attempt") == run.get("run_attempt") and record.get("event") == run.get("event")
            and record.get("checks") == {name: "success" for name in CHECKS})


def decode(raw):
    if len(raw) > MAX_BYTES:
        raise ValueError("qualification receipt archive exceeds its limit")
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        members = archive.infolist()
        if len(members) != 1 or members[0].filename != "qualification-source.json" or members[0].file_size > MAX_BYTES:
            raise ValueError("qualification receipt archive has unexpected contents")
        return json.loads(archive.read(members[0]))


def from_run(repository, run):
    rows = pages(f"repos/{repository}/actions/runs/{run['id']}/artifacts", "artifacts")
    name = f"qualification-source-{run['run_attempt']}"
    candidates = [row for row in rows if row.get("name") == name and not row.get("expired")]
    if not candidates:
        return None
    if len(candidates) != 1 or candidates[0].get("size_in_bytes", MAX_BYTES + 1) > MAX_BYTES:
        raise ValueError("qualification receipt is ambiguous or oversized")
    raw = subprocess.check_output(["gh", "api", f"repos/{repository}/actions/artifacts/{candidates[0]['id']}/zip"])
    return decode(raw)


def aggregate_passed(repository, run):
    rows = pages(f"repos/{repository}/actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs", "jobs")
    aggregate = [job for job in rows if job.get("name") == "qualification"]
    return len(aggregate) == 1 and aggregate[0].get("conclusion") == "success"


def main():
    receipt = complete(json.loads(os.environ["QUALIFICATION_RECORD"]), json.loads(os.environ["NEEDS_JSON"]), os.environ)
    Path("qualification-source.json").write_text(json.dumps(receipt, indent=2) + "\n")
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
        summary.write(f"Qualified source `{receipt['source_sha']}` with controller `{receipt['controller_sha']}`.\n")


if __name__ == "__main__":
    main()

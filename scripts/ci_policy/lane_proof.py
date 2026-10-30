"""Exact-source evidence for complete, deterministic hosted lanes."""
import hashlib
import json
import os
from pathlib import Path
import subprocess

from artifact_paths import artifact_root
from ci_policy.hosted_plan import lanes
from verification_plan import catalog

# Change-relative reports and live observations require their own execution.
REUSABLE = {"build", "contracts", "lint", "vulnerabilities", "runner", "behavior", "frontend", "native"}


def recipe(name):
    lane = lanes()[name]
    data = {"lane": lane, "go": catalog()["go"], "groups": catalog()["groups"]}
    return hashlib.sha256(json.dumps(data, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def merge_group_execution(environment):
    event = environment.get("GITHUB_EVENT_NAME")
    return event == "merge_group" or (
        event == "workflow_dispatch"
        and environment.get("GITHUB_REF", "").startswith("refs/heads/gh-readonly-queue/main/")
    )


def seal(root, record, environment):
    name = record["lane"]
    if (not merge_group_execution(environment) or name not in REUSABLE
            or record["status"] != "passed" or record.get("packages") or record.get("execution_error")):
        return None
    source = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
    if source != environment["GITHUB_SHA"]:
        raise ValueError("lane proof source differs from the merge group")
    subprocess.run(["git", "diff", "--quiet", "HEAD", "--"], cwd=root, check=True)
    tree = subprocess.check_output(["git", "rev-parse", "HEAD^{tree}"], cwd=root, text=True).strip()
    return {"version": 1, "repository": environment["GITHUB_REPOSITORY"],
            "run_id": int(environment["GITHUB_RUN_ID"]), "run_attempt": int(environment["GITHUB_RUN_ATTEMPT"]),
            "source_sha": source, "source_tree": tree, "lane": name, "shard": record["shard"],
            "recipe": recipe(name), "runner": lanes()[name].get("runner", "ubuntu-24.04"),
            "image": environment.get("ImageVersion", ""), "status": "passed", "scope": "complete",
            "seconds": round(record["finished_at"] - record["started_at"], 1)}


def publish(root, record):
    if not merge_group_execution(os.environ):
        return
    proof = seal(root, record, os.environ)
    if proof is not None:
        (Path(os.environ["RUNNER_TEMP"]) / "lane-proof.json").write_text(json.dumps(proof, indent=2) + "\n")


if __name__ == "__main__":
    root = Path(__file__).resolve().parents[2]
    publish(root, json.loads((artifact_root(root) / "ci/run.json").read_text()))

"""Bind an immutable release candidate to its trusted dispatch controller."""
import json
import os
from pathlib import Path
import re
import subprocess
import sys

from ci_policy.github import api

ROOT = Path(__file__).resolve().parents[2]


def commit(value):
    if not isinstance(value, str) or not re.fullmatch(r"[0-9a-f]{40}", value):
        raise ValueError("qualification source must be a full commit SHA")
    return subprocess.check_output(["git", "rev-parse", "--verify", value + "^{commit}"], cwd=ROOT, text=True).strip()


# Reusable workflow interfaces and checked-out execution tooling must agree.
EXECUTION_PATHS = (".github", "scripts", "task", "Taskfile.yml")


def require_compatible_tooling(source, controller):
    if source == controller:
        return
    result = subprocess.run(["git", "diff", "--quiet", source, controller, "--", *EXECUTION_PATHS], cwd=ROOT)
    if result.returncode == 1:
        raise ValueError("qualification source has different CI tooling; select a main commit with the controller's .github, scripts, task, and Taskfile.yml")
    result.check_returncode()


def select(environment):
    repository = environment["GITHUB_REPOSITORY"]
    event = environment["GITHUB_EVENT_NAME"]
    if environment["GITHUB_REF"] != "refs/heads/main" or event != "workflow_dispatch":
        raise ValueError("qualification requests must use the trusted main workflow")
    controller = commit(environment["GITHUB_WORKFLOW_SHA"])
    head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    if head != controller:
        raise ValueError("qualification controller checkout does not match its workflow revision")
    source = commit(environment.get("QUALIFICATION_SOURCE") or environment["GITHUB_SHA"])
    tip = api(f"repos/{repository}/git/ref/heads/main")["object"]["sha"]
    subprocess.run(["git", "fetch", "--no-tags", "origin", tip], cwd=ROOT, check=True)
    ancestor = subprocess.run(["git", "merge-base", "--is-ancestor", source, tip], cwd=ROOT)
    if ancestor.returncode:
        raise ValueError("qualification source is not an ancestor of current main")
    run_id = int(environment["GITHUB_RUN_ID"])
    require_compatible_tooling(source, controller)
    tree = subprocess.check_output(["git", "rev-parse", source + "^{tree}"], cwd=ROOT, text=True).strip()
    return {"version": 1, "repository": repository, "run_id": run_id,
            "run_attempt": int(environment["GITHUB_RUN_ATTEMPT"]), "event": event,
            "source_sha": source, "source_tree": tree, "controller_sha": controller}


def main():
    record = select(os.environ)
    encoded = json.dumps(record, separators=(",", ":"))
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"source-sha={record['source_sha']}\nrecord={encoded}\n")
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
        summary.write("### Qualification source\n\n```json\n" + json.dumps(record, indent=2) + "\n```\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())

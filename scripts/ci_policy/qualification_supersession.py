"""Cancel obsolete background snapshots without interrupting selected candidates."""
import json
import os
from pathlib import Path
import subprocess

from ci_policy.github import api, pages

ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = ".github/workflows/qualification.yml"
ACTIVE = ("queued", "in_progress", "waiting", "pending", "requested")


def obsolete(run, repository, tip):
    return (type(run.get("id")) is int and run["id"] > 0
            and run.get("event") == "push" and run.get("head_branch") == "main"
            and run.get("path") == WORKFLOW and run.get("status") in ACTIVE
            and run.get("head_repository", {}).get("full_name") == repository
            and run.get("head_sha") != tip)


def reconcile(environment):
    if environment["GITHUB_EVENT_NAME"] != "push" or environment["GITHUB_REF"] != "refs/heads/main":
        raise ValueError("qualification supersession must run from a main push")
    repository = environment["GITHUB_REPOSITORY"]
    tip = api(f"repos/{repository}/git/ref/heads/main")["object"]["sha"]
    subprocess.run(["git", "fetch", "--no-tags", "origin", tip], cwd=ROOT, check=True)
    cancelled = []
    seen = set()
    for status in ACTIVE:
        for run in pages(f"repos/{repository}/actions/workflows/qualification.yml/runs?event=push&branch=main&status={status}", "workflow_runs"):
            if not obsolete(run, repository, tip) or run["id"] in seen:
                continue
            seen.add(run["id"])
            # An obsolete controller may run late. Only ancestors of live main
            # can be superseded; unrelated or unavailable commits stay untouched.
            sha = run.get("head_sha")
            if not isinstance(sha, str) or len(sha) != 40 or any(c not in "0123456789abcdef" for c in sha):
                continue
            ancestor = subprocess.run(["git", "merge-base", "--is-ancestor", sha, tip], cwd=ROOT)
            if ancestor.returncode == 1:
                continue
            ancestor.check_returncode()
            current = api(f"repos/{repository}/actions/runs/{run['id']}")
            if not obsolete(current, repository, tip) or current.get("head_sha") != sha:
                continue
            api(f"repos/{repository}/actions/runs/{run['id']}/cancel", "POST")
            cancelled.append({"run_id": run["id"], "source_sha": sha})
    return {"version": 1, "repository": repository, "main_sha": tip,
            "cancelled": cancelled, "reason": "superseded background qualification"}


def main():
    record = reconcile(os.environ)
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
        summary.write("### Qualification supersession\n\n```json\n" + json.dumps(record, indent=2) + "\n```\n")
    print(json.dumps(record))


if __name__ == "__main__":
    main()

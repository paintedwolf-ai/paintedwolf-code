"""Admit diagnostic-only replay from retained release fixtures."""
import json
import os
import re
from ci_policy.github import api, pages, repository


def select(run_id, release_tag):
    if release_tag:
        raise ValueError("diagnostic replay cannot request publication")
    if not re.fullmatch(r"[1-9][0-9]*", run_id):
        raise ValueError("rehearsal run must be a positive run ID")
    repo = repository()
    run = api(f"{repo}/actions/runs/{run_id}")
    if (run.get("path") != ".github/workflows/release.yml"
            or run.get("status") != "completed"
            or run.get("event") not in {"push", "workflow_dispatch"}
            or run.get("head_repository", {}).get("full_name") != os.environ["GITHUB_REPOSITORY"]):
        raise ValueError("fixtures require a completed release run in this repository")
    attempt = run["run_attempt"]
    jobs = list(pages(f"{repo}/actions/runs/{run_id}/attempts/{attempt}/jobs", "jobs"))
    if not any(job["name"] == "prepare-bundle-upgrade" and job["conclusion"] == "success" for job in jobs):
        raise ValueError("release run did not prepare authenticated upgrade fixtures")
    name = f"bundle-upgrade-fixtures-{attempt}"
    matches = [row for row in pages(f"{repo}/actions/runs/{run_id}/artifacts", "artifacts")
               if row["name"] == name and not row["expired"]]
    if len(matches) != 1:
        raise ValueError("release fixtures are absent, expired, or ambiguous")
    return {"run_id": int(run_id), "attempt": attempt, "source_sha": run["head_sha"],
            "artifact_id": matches[0]["id"], "artifact_digest": matches[0].get("digest")}


def main():
    record = select(os.environ["REHEARSAL_RUN_ID"], os.environ.get("RELEASE_TAG", ""))
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"artifact_id={record['artifact_id']}\n")
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
        summary.write("Diagnostic only; publication is disabled.\n\n```json\n" + json.dumps(record, indent=2) + "\n```\n")


if __name__ == "__main__":
    main()

"""Use full nightly results for the exact candidate source."""

import argparse
import json
import os
from pathlib import Path
import subprocess
from urllib.parse import urlencode
import zipfile

from ci_policy.github import pages
from ci_policy.qualification_receipt import from_run, valid_identity

ARTIFACT = "nightly-source"
EVENTS = {"schedule", "workflow_dispatch"}


def identity(repository, source, tree, run_id, attempt, controller):
    return dict(repository=repository, source_sha=source, source_tree=tree,
                run_id=run_id, run_attempt=attempt, controller_sha=controller)


def write(environment):
    source = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    tree = subprocess.check_output(["git", "rev-parse", "HEAD^{tree}"], text=True).strip()
    if (source, tree) != (environment["SOURCE_SHA"], environment["SOURCE_TREE"]):
        raise ValueError("nightly checkout differs from its selected source")
    record = identity(environment["GITHUB_REPOSITORY"], source, tree, int(environment["GITHUB_RUN_ID"]),
                      int(environment["GITHUB_RUN_ATTEMPT"]), environment["GITHUB_SHA"])
    Path(ARTIFACT + ".json").write_text(json.dumps(record, indent=2) + "\n")


def matching_run(qualification):
    repository, source, tree = (qualification[key] for key in ("repository", "source_sha", "source_tree"))
    query = urlencode({"branch": "main", "status": "success"})
    path = f"repos/{repository}/actions/workflows/nightly.yml/runs?{query}"
    for run in pages(path, "workflow_runs"):
        if (run.get("display_title") != f"Nightly {source}" or run.get("head_branch") != "main"
                or run.get("event") not in EVENTS or run.get("conclusion") != "success"):
            continue
        try:
            record = from_run(repository, run, ARTIFACT)
        except (ValueError, KeyError, TypeError, OSError, zipfile.BadZipFile, subprocess.SubprocessError):
            continue
        if record == identity(repository, source, tree, run["id"], run["run_attempt"], run["head_sha"]):
            return run["id"]
    return None


def select(environment):
    qualification = json.loads(environment["QUALIFICATION_RECORD"])
    if not valid_identity(qualification):
        raise ValueError("nightly lookup requires a bound qualification source")
    try:
        run_id = matching_run(qualification)
    except (KeyError, TypeError, OSError, subprocess.SubprocessError):
        run_id = None
    if run_id:
        with open(environment["GITHUB_OUTPUT"], "a") as output:
            output.write(f"nightly-run-id={run_id}\n")
    with open(environment["GITHUB_STEP_SUMMARY"], "a") as summary:
        summary.write(f"Reusing full nightly run {run_id}.\n" if run_id
                      else "No matching full nightly. Verification runs on the candidate.\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("write", "select"))
    args = parser.parse_args()
    {"write": write, "select": select}[args.operation](os.environ)


if __name__ == "__main__":
    main()

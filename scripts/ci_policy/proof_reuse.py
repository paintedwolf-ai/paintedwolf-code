"""Reuse complete merge-group lanes for the identical landed source tree."""
import argparse
from collections import defaultdict
from datetime import datetime, timedelta, timezone
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import zipfile

from ci_policy.github import api, pages
from ci_policy.lane_proof import REUSABLE, recipe

LIMIT = 64 * 1024
FIELDS = {"version", "repository", "run_id", "run_attempt", "source_sha", "source_tree", "lane", "shard",
          "recipe", "runner", "image", "status", "scope", "seconds"}


def decode(raw):
    if len(raw) > LIMIT:
        raise ValueError("lane proof archive exceeds its limit")
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        entries = archive.infolist()
        if len(entries) != 1 or entries[0].filename != "lane-proof.json" or entries[0].file_size > LIMIT:
            raise ValueError("lane proof archive has unexpected contents")
        return json.loads(archive.read(entries[0]))


def matches(proof, row, run, repository, tree):
    return (isinstance(proof, dict) and set(proof) == FIELDS and type(proof["version"]) is int and proof["version"] == 1
            and proof["repository"] == repository and proof["source_tree"] == tree
            and proof["source_sha"] == run["head_sha"] and proof["run_id"] == run["id"]
            and proof["run_attempt"] == run["run_attempt"] and proof["status"] == "passed" and proof["scope"] == "complete"
            and proof["lane"] == row["lane"] and proof["shard"] == row["shard"]
            and proof["runner"] == row["runner"] and proof["recipe"] == recipe(row["lane"]))


def select(matrix, proofs, run, repository, tree):
    groups = defaultdict(list)
    for row in matrix["include"]:
        groups[row["lane"]].append(row)
    reused = []
    for lane, rows in groups.items():
        if lane not in REUSABLE:
            continue
        matching = [[proof for proof in proofs if matches(proof, row, run, repository, tree)] for row in rows]
        # Every shard comes from one successful execution and its shared partition.
        if all(len(values) == 1 for values in matching):
            reused.extend(values[0] for values in matching)
    names = {proof["lane"] for proof in reused}
    return {"include": [row for row in matrix["include"] if row["lane"] not in names]}, reused


def candidates(repository, tree):
    since = (datetime.now(timezone.utc) - timedelta(days=14)).strftime("%Y-%m-%dT%H:%M:%SZ")
    query = f"event=merge_group&status=success&created=%3E%3D{since}&per_page=20"
    runs = api(f"repos/{repository}/actions/workflows/ci.yml/runs?{query}")["workflow_runs"]
    for run in runs:
        if (run.get("event") != "merge_group" or run.get("conclusion") != "success"
                or run.get("path") != ".github/workflows/ci.yml"
                or (run.get("head_repository") or {}).get("full_name") != repository):
            continue
        commit = api(f"repos/{repository}/git/commits/{run['head_sha']}")
        if commit["tree"]["sha"] == tree:
            yield run


def read_proofs(repository, run):
    proofs = []
    for artifact in pages(f"repos/{repository}/actions/runs/{run['id']}/artifacts", "artifacts"):
        if (not artifact["name"].startswith("lane-proof-") or artifact.get("expired")
                or artifact.get("size_in_bytes", LIMIT + 1) > LIMIT):
            continue
        raw = subprocess.check_output(["gh", "api", f"repos/{repository}/actions/artifacts/{artifact['id']}/zip"], timeout=30)
        proofs.append(decode(raw))
    return proofs


def resolve(matrix, repository, source, tree):
    audit = {"version": 1, "source_sha": source, "source_tree": tree, "reused": [], "lookup_errors": []}
    remaining = matrix
    try:
        for run in candidates(repository, tree):
            proofs = read_proofs(repository, run)
            remaining, accepted = select(remaining, proofs, run, repository, tree)
            audit["reused"].extend(accepted)
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError, zipfile.BadZipFile) as error:
        # Missing evidence costs execution time; it never grants a pass.
        audit["lookup_errors"].append(str(error))
    if not remaining["include"]:
        raise ValueError("qualification must execute its change-relative reporting lane")
    audit["execute"] = [{"lane": row["lane"], "shard": row["shard"]} for row in remaining["include"]]
    return remaining, audit


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--matrix", required=True)
    parser.add_argument("--source", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    source = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    if source != args.source:
        raise ValueError("qualification reuse requires the selected source checkout")
    tree = subprocess.check_output(["git", "rev-parse", "HEAD^{tree}"], text=True).strip()
    matrix, audit = resolve(json.loads(args.matrix), os.environ["GITHUB_REPOSITORY"], source, tree)
    args.output.write_text(json.dumps(audit, indent=2) + "\n")
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a") as stream:
            stream.write("\n### Reused merge-group verification\n\n")
            for proof in audit["reused"]:
                url = f"https://github.com/{proof['repository']}/actions/runs/{proof['run_id']}"
                stream.write(f"- {proof['lane']} {proof['shard']}: [run {proof['run_id']}]({url}), attempt {proof['run_attempt']}.\n")
            if not audit["reused"]:
                stream.write("No complete matching evidence; all selected lanes execute.\n")
    print(json.dumps(matrix, separators=(",", ":")))


if __name__ == "__main__":
    main()

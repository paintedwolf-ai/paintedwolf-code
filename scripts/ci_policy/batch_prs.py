"""Combine PR heads in an isolated worktree and open one PR for normal CI."""
import argparse
from pathlib import Path
import re
import subprocess
import tempfile
import uuid

from ci_policy.github import api

ROOT = Path(__file__).resolve().parents[2]
SOURCE_QUERY = """query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) { pullRequest(number: $number) {
    id number url state headRefOid isDraft autoMergeRequest { enabledAt }
    mergeQueueEntry { id }
  } }
}"""


def command(arguments, cwd=ROOT):
    return subprocess.check_output(arguments, cwd=cwd, text=True).strip()


def source(repository, number):
    owner, name = repository.split("/")
    result = api("graphql", "POST", {"query": SOURCE_QUERY,
                 "variables": {"owner": owner, "name": name, "number": number}})
    row = result["data"]["repository"]["pullRequest"]
    if row["number"] != number or row["state"] != "OPEN":
        raise ValueError(f"PR #{number} must be open")
    if not re.fullmatch(r"[0-9a-f]{40}", row["headRefOid"]):
        raise ValueError(f"PR #{number} has no immutable commit identity")
    return row


def withdraw(repository, row):
    current = source(repository, row["number"])
    if current["headRefOid"] != row["headRefOid"] or current["isDraft"] != row["isDraft"]:
        raise ValueError(f"PR #{row['number']} changed before queue withdrawal; batch remains draft")
    if current["mergeQueueEntry"] is not None:
        api("graphql", "POST", {"query": """mutation($id: ID!) {
          dequeuePullRequest(input: {id: $id}) { clientMutationId }
        }""", "variables": {"id": current["id"]}})
        current = source(repository, row["number"])
        if current["headRefOid"] != row["headRefOid"] or current["isDraft"] != row["isDraft"]:
            raise ValueError(f"PR #{row['number']} changed during queue withdrawal")
        if current["mergeQueueEntry"] is not None:
            raise ValueError(f"PR #{row['number']} remains in the merge queue")
    if current["autoMergeRequest"] is not None:
        api("graphql", "POST", {"query": """mutation($id: ID!) {
          disablePullRequestAutoMerge(input: {pullRequestId: $id}) { clientMutationId }
        }""", "variables": {"id": current["id"]}})


def body(base, rows):
    lines = ["Combine these changes for one verification and merge-queue cycle.", "",
             f"Base: `{base}`", "", "| PR | Included head |", "|---|---|"]
    lines.extend(f"| {row['url']} | `{row['headRefOid']}` |" for row in rows)
    lines += ["", "The combined result requires the normal fast tier and merge-queue checks.",
              "Source PRs stay open until this batch lands; preserve later source commits when closing them."]
    return "\n".join(lines) + "\n"


def create(repository, numbers):
    if len(numbers) < 2 or len(numbers) != len(set(numbers)) or any(number < 1 for number in numbers):
        raise ValueError("provide at least two distinct positive PR numbers, in dependency order")
    rows = [source(repository, number) for number in numbers]
    # Fetch immutable heads rather than moving branch names.
    base = command(["git", "ls-remote", "--exit-code", "origin", "refs/heads/main"]).split()[0]
    if not re.fullmatch(r"[0-9a-f]{40}", base):
        raise ValueError("origin main has no immutable commit identity")
    command(["git", "fetch", "--no-tags", "--no-write-fetch-head", "origin", base,
             *[row["headRefOid"] for row in rows]])
    branch = "batch/" + uuid.uuid4().hex[:12]
    directory = Path(tempfile.mkdtemp(prefix="paintedwolf-batch-"))
    checkout = directory / "checkout"
    command(["git", "worktree", "add", "-b", branch, str(checkout), base])
    print(f"Batch worktree: {checkout}", flush=True)
    # A conflict stops here with the worktree retained, before any push or PR.
    for row in rows:
        command(["git", "merge", "--no-ff", "--no-edit", "--signoff", row["headRefOid"]], checkout)
    for row in rows:
        current = source(repository, row["number"])
        if current["headRefOid"] != row["headRefOid"] or current["isDraft"] != row["isDraft"]:
            raise ValueError(f"PR #{row['number']} changed while building the batch; rebuild from current heads")
    description = directory / "body.md"
    description.write_text(body(base, rows))
    command(["git", "push", "-u", "origin", branch], checkout)
    url = command(["gh", "pr", "create", "--repo", repository, "--base", "main", "--head", branch,
                   "--title", "Batch " + ", ".join(f"#{number}" for number in numbers),
                   "--body-file", str(description), "--draft"], checkout)
    print(url, flush=True)
    for row in rows:
        withdraw(repository, row)
    if not any(row["isDraft"] for row in rows):
        command(["gh", "pr", "ready", url], checkout)
    return url


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("prs", nargs="+", type=int, help="PR numbers in dependency order; every change type is allowed")
    args = parser.parse_args()
    repository = command(["gh", "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner"])
    create(repository, args.prs)


if __name__ == "__main__":
    main()

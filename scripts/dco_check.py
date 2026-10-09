#!/usr/bin/env python3
"""Certify original PR commits using trusted source and exact queue membership."""
import json
import os
from pathlib import Path
import re
import subprocess
import sys


CHECK_NAME = "DCO-owned"
PAGE = "pageInfo { hasNextPage endCursor } totalCount"
PR_FIELDS = "number headRefOid baseRefOid isDraft state"


class Refused(RuntimeError):
    """Incomplete or changed authority cannot certify a commit."""


class GitHub:
    def __init__(self, repository):
        self.repository = repository
        self.owner, self.name = repository.split("/")

    def api(self, path, payload=None, method=None):
        command = ["gh", "api", path]
        if method:
            command += ["--method", method]
        if payload is not None:
            command += ["--input", "-"]
        result = subprocess.run(command, input=json.dumps(payload) if payload is not None else None,
                                text=True, capture_output=True, check=True)
        response = json.loads(result.stdout)
        if isinstance(response, dict) and response.get("errors"):
            raise Refused("GitHub returned GraphQL errors")
        return response

    def query(self, body, **variables):
        query = "query($owner:String!,$name:String!,$cursor:String,$number:Int!,$branch:String) { repository(owner:$owner,name:$name) { " + body + " } }"
        # GraphQL rejects declared but unused variables.
        declarations = {"cursor": "String", "number": "Int!", "branch": "String"}
        for key, kind in declarations.items():
            if "$" + key not in body:
                query = query.replace(",$" + key + ":" + kind, "")
        return self.api("graphql", {"query": query, "variables": {"owner": self.owner, "name": self.name, **variables}})["data"]["repository"]

    def pull(self, number):
        return self.query("pullRequest(number:$number) { " + PR_FIELDS + " }", number=number)["pullRequest"]

    def commits(self, number):
        pr = self.pull(number)
        identity = {key: pr[key] for key in ("headRefOid", "baseRefOid", "isDraft", "state")}
        if identity["isDraft"] or identity["state"] != "OPEN":
            raise Refused("PR is draft or no longer open")
        # PR commit connections truncate at 250, even when GraphQL totalCount is larger.
        # Immutable paginated comparisons include every commit reachable only from head.
        commits, seen, total, page = [], set(), None, 1
        while True:
            response = self.api(f"repos/{self.repository}/compare/{identity['baseRefOid']}...{identity['headRefOid']}?per_page=100&page={page}")
            if response["base_commit"]["sha"] != identity["baseRefOid"]:
                raise Refused("Comparison base differs from captured PR base")
            if total is not None and total != response["total_commits"]:
                raise Refused("Comparison commit count changed")
            total = response["total_commits"]
            nodes = response["commits"]
            for node in nodes:
                if node["sha"] in seen:
                    raise Refused("Repeated commit in paginated comparison")
                seen.add(node["sha"])
                commits.append({"oid": node["sha"], **node["commit"], "parents": {"totalCount": len(node["parents"])},
                                "githubAuthor": node.get("author")})
            if len(commits) == total:
                break
            if len(nodes) != 100 or len(commits) > total:
                raise Refused("Incomplete paginated comparison")
            page += 1
        if not commits or identity["headRefOid"] not in seen or self.pull(number) != pr:
            raise Refused("Incomplete or changed PR commit inventory")
        return identity, commits

    def queue(self, branch):
        cursor, entries, total = None, [], None
        while True:
            queue = self.query("mergeQueue(branch:$branch) { entries(first:100,after:$cursor) { " + PAGE +
                               " nodes { id baseCommit { oid } headCommit { oid } pullRequest { " + PR_FIELDS + " } } } }",
                               branch=branch, cursor=cursor)["mergeQueue"]
            if queue is None:
                raise Refused("Merge queue is unavailable")
            connection = queue["entries"]
            if total is not None and total != connection["totalCount"]:
                raise Refused("Merge queue changed during pagination")
            total = connection["totalCount"]
            entries += connection["nodes"]
            if len(entries) > total or len({entry["id"] for entry in entries}) != len(entries):
                raise Refused("Repeated merge queue page")
            next_cursor = next_page(connection, cursor, bool(connection["nodes"]))
            if next_cursor is None:
                break
            cursor = next_cursor
        if len(entries) != total or len({entry["id"] for entry in entries}) != total:
            raise Refused("Incomplete merge queue inventory")
        return entries


def next_page(connection, cursor, populated):
    page = connection["pageInfo"]
    if not page["hasNextPage"]:
        return None
    following = page["endCursor"]
    if not populated or not following or following == cursor:
        raise Refused("Pagination stopped before all commits were fetched")
    return following


def queue_members(entries, base, head):
    by_head = {}
    for entry in entries:
        commit = entry["headCommit"]
        if commit is not None:
            if commit["oid"] in by_head:
                raise Refused("Ambiguous merge queue commit")
            by_head[commit["oid"]] = entry
    members, visited = [], set()
    while head != base:
        if head in visited or head not in by_head:
            raise Refused("Merge group cannot be mapped to original PRs")
        visited.add(head)
        entry = by_head[head]
        if entry["baseCommit"] is None or entry["pullRequest"] is None:
            raise Refused("Incomplete merge queue entry")
        members.append(entry["pullRequest"])
        head = entry["baseCommit"]["oid"]
    if not members:
        raise Refused("Merge group contains no PRs")
    return list(reversed(members))


def signoff_valid(commit):
    if commit["parents"]["totalCount"] > 1:
        return True
    identities = [commit["author"], commit["committer"]]
    pairs = {(identity["name"].casefold(), identity["email"].casefold())
             for identity in identities if identity.get("name") and identity.get("email")}
    signoffs = re.findall(r"^Signed-off-by: (.+) <([^<>\s]+)>\s*$", commit["message"], re.MULTILINE | re.IGNORECASE)
    return any((name.casefold(), email.casefold()) in pairs and
               re.fullmatch(r"[^@\s]+@[^@\s]+\.[^@\s]+", email) for name, email in signoffs)


def certify_pull(github, expected):
    identity, commits = github.commits(expected["number"])
    if identity["isDraft"] or identity["state"] != "OPEN":
        raise Refused("PR is draft or no longer open")
    if identity["headRefOid"] != expected["headRefOid"] or identity["baseRefOid"] != expected["baseRefOid"]:
        raise Refused("PR head changed before certification")
    failed = []
    for commit in commits:
        if not signoff_valid(commit):
            # Bot status comes from GitHub identity, never an email/login heuristic.
            if (commit.get("githubAuthor") or {}).get("type") != "Bot":
                failed.append(commit["oid"])
    if github.pull(expected["number"]) != {"number": expected["number"], **identity}:
        raise Refused("PR changed before publication")
    return failed


def target(event_name, event, github):
    if event_name in ("pull_request_target", "workflow_dispatch"):
        pr = event["pull_request"] if event_name == "pull_request_target" else github.pull(int(event["inputs"]["pull_request"]))
        if pr.get("draft", pr.get("isDraft", False)):
            return None
        expected = {"number": pr["number"], "headRefOid": pr.get("headRefOid") or pr["head"]["sha"]}
        return expected["headRefOid"], [expected], None
    if event_name == "merge_group":
        group = event["merge_group"]
        branch = group["base_ref"].removeprefix("refs/heads/")
        members = queue_members(github.queue(branch), group["base_sha"], group["head_sha"])
        return group["head_sha"], members, (branch, group["base_sha"], group["head_sha"])
    raise Refused("Unsupported DCO event")


def run(github, event_name, event):
    selected = target(event_name, event, github)
    if selected is None:
        print("Draft PR left untouched")
        return 0
    sha, members, group = selected
    for member in members:
        current = github.pull(member["number"])
        if current["isDraft"]:
            print("Draft PR left untouched")
            return 0
        if current["state"] != "OPEN" or current["headRefOid"] != member["headRefOid"] or current["baseRefOid"] != member.get("baseRefOid", current["baseRefOid"]):
            raise Refused("PR changed before check creation")
        if "baseRefOid" in member and current["baseRefOid"] != member["baseRefOid"]:
            raise Refused("PR base changed before certification")
        member["baseRefOid"] = current["baseRefOid"]
    conclusion, summary = "failure", "Certification did not complete"
    try:
        failed = [oid for member in members for oid in certify_pull(github, member)]
        if group and queue_members(github.queue(group[0]), group[1], group[2]) != members:
            raise Refused("Merge group membership changed before publication")
        # Recheck all members after the complete group scan, including draft transitions.
        for member in members:
            current = github.pull(member["number"])
            if current["isDraft"]:
                print("PR returned to draft; check left untouched")
                return 1
            if current["state"] != "OPEN" or current["headRefOid"] != member["headRefOid"] or current["baseRefOid"] != member.get("baseRefOid", current["baseRefOid"]):
                raise Refused("PR changed before group publication")
        conclusion = "failure" if failed else "success"
        summary = "Missing or mismatched sign-off: " + ", ".join(failed) if failed else "All original PR commits have valid sign-offs or default bot/merge exemptions."
    except (Refused, KeyError, TypeError, subprocess.SubprocessError) as error:
        summary = "DCO certification refused: " + str(error)
    try:
        for member in members:
            current = github.pull(member["number"])
            if current["isDraft"]:
                print("PR returned to draft; check left untouched")
                return 1
            if current["state"] != "OPEN" or current["headRefOid"] != member["headRefOid"] or current["baseRefOid"] != member.get("baseRefOid", current["baseRefOid"]):
                print("PR changed; no check published")
                return 1
    except (Refused, KeyError, TypeError, subprocess.SubprocessError):
        print("Cannot establish live draft state; no check published")
        return 1
    github.api(f"repos/{github.repository}/check-runs", {
        "name": CHECK_NAME, "head_sha": sha, "status": "completed", "conclusion": conclusion,
        "details_url": os.environ.get("DCO_RUN_URL", "https://github.com/" + github.repository),
        "output": {"title": "Original commit certification", "summary": summary[:60000]}})
    print(summary)
    return 0 if conclusion == "success" else 1


if __name__ == "__main__":
    try:
        sys.exit(run(GitHub(os.environ["GITHUB_REPOSITORY"]), os.environ["GITHUB_EVENT_NAME"],
                     json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())))
    except (Refused, KeyError, TypeError, subprocess.SubprocessError) as error:
        print(f"DCO check failed closed: {error}", file=sys.stderr)
        sys.exit(1)

"""Bind unsigned release artifacts to their exact source, and find a preparation to reuse."""

from __future__ import annotations

import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
from urllib.parse import quote
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from ci_policy.github import api, pages
from linux_release.candidates import matrix
from linux_release.nix_runner import SYSTEMS
from release_distribution_contract.platforms import parse_platforms

FORMAT = 1
STAMP = "preparation.json"
RECEIPT = "release-preparation-"
KINDS = ("linux-packages", "nix-closure", "macos-app")
STAMP_FIELDS = {"format_version", "name", "kind", "source_commit", "source_tree", "digest", "files"}
RECEIPT_FIELDS = {"format_version", "repository", "run_id", "run_attempt", "controller_commit",
                  "source_commit", "source_tree", "artifacts"}
ENTRY_FIELDS = {"name", "kind", "artifact_id", "digest"}
# Only runs whose workflow definitions come from main may vouch for artifacts.
TRUSTED_EVENTS = {"push", "schedule", "workflow_dispatch"}
RECEIPT_LIMIT = 64 * 1024
OBJECT = re.compile("[0-9a-f]{40}")
DIGEST = re.compile("sha256:[0-9a-f]{64}")


class Refused(ValueError):
    """An artifact or receipt is not bound to the requested source."""


def plan(catalog: dict) -> dict:
    linux = matrix(catalog)["include"]
    darwin = [row for row in parse_platforms(json.dumps(catalog).encode()).values()
              if row.get("goos") == "darwin" and row.get("publication") == "public"]
    if len(darwin) != 1:
        raise ValueError("preparation requires exactly one public macOS row")
    nix = [{"system": SYSTEMS[row["artifact_arch"]], "runner": row["runner"]} for row in linux]
    return {"linux": {"include": linux}, "nix": {"include": nix}, "macos": {"include": darwin}}


def names(prepared: dict) -> dict[str, str]:
    return {**{"prepared-" + row["updater_key"]: "linux-packages" for row in prepared["linux"]["include"]},
            **{"prepared-nix-" + row["system"]: "nix-closure" for row in prepared["nix"]["include"]},
            **{"prepared-" + row["updater_key"]: "macos-app" for row in prepared["macos"]["include"]}}


def git(*arguments: str) -> str:
    return subprocess.check_output(["git", *arguments], text=True).strip()


def source_tree(commit: str) -> str:
    if not OBJECT.fullmatch(commit):
        raise Refused("source commit must be a full object name")
    return git("rev-parse", commit + "^{tree}")


def inventory(root: Path) -> list[dict]:
    rows = []
    for path in sorted(root.rglob("*")):
        relative = path.relative_to(root).as_posix()
        mode = path.lstat().st_mode
        if stat.S_ISDIR(mode) or relative == STAMP:
            continue
        if not stat.S_ISREG(mode):
            raise Refused(f"{relative} is not a regular file")
        digest = hashlib.sha256()
        with path.open("rb") as stream:
            for block in iter(lambda: stream.read(1 << 20), b""):
                digest.update(block)
        rows.append({"path": relative, "sha256": digest.hexdigest(), "bytes": path.stat().st_size})
    if not rows:
        raise Refused("an artifact must contain files")
    return rows


def content_digest(rows: list[dict]) -> str:
    lines = "".join(f"{row['sha256']}  {row['bytes']}  {row['path']}\n" for row in rows)
    return "sha256:" + hashlib.sha256(lines.encode()).hexdigest()


def stamp(root: Path, name: str, kind: str, commit: str) -> dict:
    if kind not in KINDS:
        raise ValueError(f"unknown artifact kind {kind!r}")
    if git("rev-parse", "HEAD") != commit:
        raise Refused("the checkout is not the requested source commit")
    rows = inventory(root)
    record = {"format_version": FORMAT, "name": name, "kind": kind, "source_commit": commit,
              "source_tree": source_tree(commit), "digest": content_digest(rows), "files": rows}
    (root / STAMP).write_text(json.dumps(record, indent=1) + "\n")
    return record


def read_stamp(path: Path) -> dict:
    record = json.loads(path.read_text())
    if (not isinstance(record, dict) or set(record) != STAMP_FIELDS or record["format_version"] != FORMAT
            or record["kind"] not in KINDS or not isinstance(record["name"], str)
            or not all(isinstance(record[key], str) and OBJECT.fullmatch(record[key])
                       for key in ("source_commit", "source_tree"))
            or not isinstance(record["digest"], str) or not DIGEST.fullmatch(record["digest"])
            or not isinstance(record["files"], list)):
        raise Refused(f"{path} is not a preparation stamp")
    return record


def verify(root: Path, name: str, commit: str, tree: str, digest: str | None = None) -> dict:
    """Refuse an artifact unless its recorded source and bytes are exactly the requested ones."""
    record = read_stamp(root / STAMP)
    if record["name"] != name:
        raise Refused(f"artifact {record['name']!r} is not {name!r}")
    if digest is not None and record["digest"] != digest:
        raise Refused(f"{name} is not the preparation its receipt names")
    if record["source_commit"] != commit or record["source_tree"] != tree:
        raise Refused(f"{name} was prepared from {record['source_commit']}, not {commit}")
    rows = inventory(root)
    if rows != record["files"] or content_digest(rows) != record["digest"]:
        raise Refused(f"{name} differs from the bytes its preparation recorded")
    return record


def receipt(stamps: Path, prepared: dict, commit: str, environment, listed: list[dict]) -> dict:
    expected = names(prepared)
    tree = source_tree(commit)
    ids = {row["name"]: row["id"] for row in listed if row.get("expired") is False}
    entries = []
    for name, kind in sorted(expected.items()):
        record = read_stamp(stamps / name / STAMP)
        if (record["name"], record["kind"], record["source_commit"], record["source_tree"]) != (name, kind, commit, tree):
            raise Refused(f"{name} stamp is not bound to {commit}")
        if type(ids.get(name)) is not int:
            raise Refused(f"{name} was not retained by this run")
        entries.append({"name": name, "kind": kind, "artifact_id": ids[name], "digest": record["digest"]})
    return {"format_version": FORMAT, "repository": environment["GITHUB_REPOSITORY"],
            "run_id": int(environment["GITHUB_RUN_ID"]), "run_attempt": int(environment["GITHUB_RUN_ATTEMPT"]),
            "controller_commit": environment["GITHUB_SHA"], "source_commit": commit, "source_tree": tree,
            "artifacts": entries}


def accepted(record, run: dict, retained: list[dict], repository: str, commit: str, tree: str,
             expected: dict[str, str]) -> bool:
    if (not isinstance(record, dict) or set(record) != RECEIPT_FIELDS or record["format_version"] != FORMAT
            or record["repository"] != repository or record["run_id"] != run["id"]
            or record["controller_commit"] != run["head_sha"]
            or record["source_commit"] != commit or record["source_tree"] != tree
            or not isinstance(record["artifacts"], list)):
        return False
    live = {row["id"]: row["name"] for row in retained if row.get("expired") is False}
    entries = record["artifacts"]
    return (all(isinstance(row, dict) and set(row) == ENTRY_FIELDS for row in entries)
            and {row["name"]: row["kind"] for row in entries} == expected and len(entries) == len(expected)
            and all(live.get(row["artifact_id"]) == row["name"] and isinstance(row["digest"], str)
                    and DIGEST.fullmatch(row["digest"]) for row in entries))


def decode(raw: bytes) -> dict:
    if len(raw) > RECEIPT_LIMIT:
        raise Refused("receipt archive exceeds its limit")
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        entries = archive.infolist()
        if len(entries) != 1 or entries[0].filename != "receipt.json" or entries[0].file_size > RECEIPT_LIMIT:
            raise Refused("receipt archive has unexpected contents")
        return json.loads(archive.read(entries[0]))


def on_main(repository: str, commit: str) -> bool:
    comparison = api(f"repos/{repository}/compare/{commit}...main")
    return comparison.get("status") in {"identical", "ahead"}


def find(repository: str, commit: str, prepared: dict, *, fetch=None, run_id: int | None = None) -> dict | None:
    """Return the newest trusted receipt for exactly this commit and tree whose artifacts are all retained,
    from `run_id` when given."""
    fetch = fetch or (lambda artifact: subprocess.check_output(
        ["gh", "api", f"repos/{repository}/actions/artifacts/{artifact}/zip"]))
    tree = source_tree(commit)
    expected = names(prepared)
    identity = api(f"repos/{repository}")["id"]
    rows = api(f"repos/{repository}/actions/artifacts?name={quote(RECEIPT + commit)}&per_page=100")["artifacts"]
    for row in sorted(rows, key=lambda item: item["id"], reverse=True):
        origin = row.get("workflow_run") or {}
        if (row.get("name") != RECEIPT + commit or row.get("expired") is not False
                or not 0 < row.get("size_in_bytes", 0) <= RECEIPT_LIMIT
                or origin.get("repository_id") != identity or origin.get("head_repository_id") != identity
                or run_id is not None and origin.get("id") != run_id):
            continue
        run = api(f"repos/{repository}/actions/runs/{origin['id']}")
        if (run.get("event") not in TRUSTED_EVENTS or not isinstance(run.get("head_sha"), str)
                or not on_main(repository, run["head_sha"])):
            continue
        record = decode(fetch(row["id"]))
        retained = list(pages(f"repos/{repository}/actions/runs/{run['id']}/artifacts", "artifacts"))
        if accepted(record, run, retained, repository, commit, tree, expected):
            return record
    return None


def write_output(**values: str) -> None:
    with open(os.environ["GITHUB_OUTPUT"], "a") as stream:
        stream.writelines(f"{key}={value}\n" for key, value in values.items())


def summarize(text: str) -> None:
    path = os.environ.get("GITHUB_STEP_SUMMARY")
    if path:
        with open(path, "a") as stream:
            stream.write(text)


def index(record: dict) -> str:
    """Each artifact's id and digest by name, as consumers select and verify them."""
    return json.dumps({row["name"]: {"id": row["artifact_id"], "digest": row["digest"]} for row in record["artifacts"]},
                      sort_keys=True, separators=(",", ":"))


def resolve(commit: str, prepared: dict, run_id: int | None = None) -> None:
    """Reuse a trusted preparation of the commit, or build one. A required run is reused or refused, never rebuilt."""
    repository = os.environ["GITHUB_REPOSITORY"]
    if run_id is not None:
        record = find(repository, commit, prepared, run_id=run_id)
        if not record:
            raise Refused(f"run {run_id} holds no retained preparation of {commit}; qualify the source again")
    else:
        try:
            record = find(repository, commit, prepared)
        except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError, zipfile.BadZipFile) as error:
            # A failed lookup costs a build; it never grants reuse.
            print(f"::warning title=Preparation lookup::{error}")
            record = None
    if record:
        url = f"https://github.com/{repository}/actions/runs/{record['run_id']}"
        summarize(f"Reusing the preparation of `{commit}` from [run {record['run_id']}]({url}); nothing compiles.\n")
        write_output(run_id=str(record["run_id"]), reused="true", artifacts=index(record))
    else:
        summarize(f"No retained preparation of `{commit}`; every artifact builds.\n")
        write_output(run_id=os.environ["GITHUB_RUN_ID"], reused="false")


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    command = commands.add_parser("plan", help="print the artifact matrices from the platform catalog")
    command.add_argument("--catalog", type=Path, default=Path("packaging/release-platforms.json"))
    command = commands.add_parser("resolve", help="find a reusable preparation of the source commit")
    command.add_argument("--source-commit", required=True)
    command.add_argument("--plan", required=True)
    command.add_argument("--run-id", type=int, help="the preparation Qualification checked; refused unless retained")
    command = commands.add_parser("stamp", help="bind an artifact directory to the checked-out source")
    command.add_argument("--artifact", type=Path, required=True)
    command.add_argument("--name", required=True)
    command.add_argument("--kind", required=True, choices=KINDS)
    command.add_argument("--source-commit", required=True)
    command = commands.add_parser("receipt", help="list this run's stamped artifacts")
    command.add_argument("--stamps", type=Path, required=True)
    command.add_argument("--plan", required=True)
    command.add_argument("--source-commit", required=True)
    command.add_argument("--output", type=Path, required=True)
    command = commands.add_parser("verify", help="refuse an artifact not prepared from the source commit")
    command.add_argument("--artifact", type=Path, required=True)
    command.add_argument("--name", required=True)
    command.add_argument("--source-commit", required=True)
    command.add_argument("--digest", help="the content digest the preparation receipt names")
    args = parser.parse_args(argv)
    try:
        if args.command == "plan":
            print(json.dumps(plan(json.loads(args.catalog.read_text())), separators=(",", ":")))
        elif args.command == "resolve":
            resolve(args.source_commit, json.loads(args.plan), args.run_id)
        elif args.command == "stamp":
            record = stamp(args.artifact, args.name, args.kind, args.source_commit)
            print(f"{record['name']}: {record['digest']} from {record['source_commit']}")
        elif args.command == "receipt":
            listed = list(pages(f"repos/{os.environ['GITHUB_REPOSITORY']}/actions/runs/{os.environ['GITHUB_RUN_ID']}/artifacts",
                                "artifacts"))
            record = receipt(args.stamps, json.loads(args.plan), args.source_commit, os.environ, listed)
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(json.dumps(record, indent=1) + "\n")
            write_output(artifacts=index(record))
            summarize("| Artifact | Digest |\n|---|---|\n"
                      + "".join(f"| {row['name']} | `{row['digest']}` |\n" for row in record["artifacts"]))
        else:
            record = verify(args.artifact, args.name, args.source_commit, source_tree(args.source_commit), args.digest)
            print(f"{record['name']}: {record['digest']} from {record['source_commit']}")
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    return 0


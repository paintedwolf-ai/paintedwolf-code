"""Prepare reusable compiler payloads and report native cache lookup results."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess


def status(records, event):
    if not isinstance(records, dict) or not records:
        raise ValueError("cache lookup requires family results")
    active = [row for row in records.values() if row["active"] is True]
    if not active:
        raise ValueError("cache lookup has no active families")
    warm = any(row.get("hit") != "true" for row in active)
    maintenance = event in {"schedule", "workflow_dispatch"}
    return dict(warm=warm, maintenance=maintenance, prepare=warm or maintenance)


def report_status():
    records = json.loads(os.environ["CACHE_HITS"])
    state = status(records, os.environ["GITHUB_EVENT_NAME"])
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        for name, value in state.items():
            output.write(f"{name}={str(value).lower()}\n")
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
        summary.write("### Native cache lookup\n\n| Family | Result |\n|---|---|\n")
        for name, row in records.items():
            if row["active"] is True:
                summary.write(f"| {name} | {'exact hit' if row.get('hit') == 'true' else 'miss'} |\n")
            elif row.get("deferred") is True:
                reason = "deferred to preparation" if state["prepare"] else "not needed"
                summary.write(f"| {name} | {reason}; not probed |\n")


def trim_go(path, limit=1024**3):
    entries = sorted((p for p in path.rglob("*") if p.is_file() and not p.is_symlink()),
                     key=lambda p: (p.stat().st_mtime_ns, str(p)))
    total = sum(p.stat().st_size for p in entries)
    for entry in entries:
        if total <= limit:
            break
        if re.fullmatch(r"[0-9a-f]{64}-[ad]", entry.name):
            total -= entry.stat().st_size
            entry.unlink()
    return total


def prune_workspace(manifest, target=None):
    metadata = subprocess.run(["cargo", "metadata", "--manifest-path", str(manifest), "--no-deps", "--locked",
                               "--offline", "--format-version", "1"], capture_output=True, text=True, check=True)
    data = json.loads(metadata.stdout)
    members = set(data["workspace_members"])
    packages = {package["id"]: package["name"] for package in data["packages"]}
    if not members or not members.issubset(packages):
        raise ValueError("cache pruning requires complete workspace membership")
    command = ["cargo", "clean", "--manifest-path", str(manifest), "--locked", "--offline"]
    if target:
        command.extend(["--target", target])
    for member in sorted(members):
        command.extend(["--package", packages[member]])
    subprocess.run(command, check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["status", "go", "workspace"])
    parser.add_argument("path", type=Path, nargs="?")
    parser.add_argument("--target")
    args = parser.parse_args()
    if args.command == "status":
        report_status()
    elif args.path is None:
        parser.error("compiler preparation requires a path")
    elif args.command == "go":
        trim_go(args.path)
    else:
        prune_workspace(args.path, args.target)


if __name__ == "__main__":
    main()

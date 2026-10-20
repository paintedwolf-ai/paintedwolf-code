"""Execute a fixed installed leaf on explicitly selected disposable guests."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys

from . import installed_evidence as evidence
from .installed_execution import read, registry
from .installed_results import admit
from .installed_transfer import receive
from .support_matrix import CATALOG, expand

FIELDS = {"id", "host", "port", "user", "identity_file", "known_hosts", "checkout",
          "runtime_bin_dirs", "artifact_id", "environment_id", "final_artifacts", "file_root",
          "install_root", "output", "nix_program", "case_inputs"}


def absolute(value):
    if not isinstance(value, str) or not Path(value).is_absolute() or "\0" in value or "\n" in value:
        raise ValueError("guest path must be an explicit absolute path")
    return value


def command(target: dict, arguments: list[str]) -> list[str]:
    if set(target) != FIELDS:
        raise ValueError("guest target does not match the fixed execution contract")
    if not evidence.IDENTIFIER.fullmatch(target["id"]) or not re.fullmatch(r"[A-Za-z0-9_.-]+", target["host"]):
        raise ValueError("guest identity is invalid")
    if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_-]*", target["user"]) or type(target["port"]) is not int or not 0 < target["port"] < 65536:
        raise ValueError("guest SSH endpoint is invalid")
    bins = target["runtime_bin_dirs"]
    if not isinstance(bins, list) or len(bins) > 8 or any(":" in absolute(item) for item in bins):
        raise ValueError("guest runtime directories are invalid")
    path = ":".join([*bins, "/usr/local/bin", "/usr/bin", "/bin"])
    script = "cd " + shlex.quote(absolute(target["checkout"])) + " && exec env " + shlex.quote("PATH=" + path) + " " + shlex.join(arguments)
    return ["ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "IdentitiesOnly=yes",
            "-o", "UserKnownHostsFile=" + absolute(target["known_hosts"]), "-i", absolute(target["identity_file"]),
            "-p", str(target["port"]), target["user"] + "@" + target["host"], script]


def run_guest(target: dict, source: str, tree: str, output: Path) -> Path:
    arguments = ["./task", "test:linux:installed", "--", "--run-cases", "--source-commit", source,
                 "--driver-tree", tree, "--artifact-id", target["artifact_id"], "--environment-id", target["environment_id"]]
    for field in ("final_artifacts", "file_root", "install_root", "output", "nix_program", "case_inputs"):
        value = target[field]
        if value is None and field in {"nix_program", "case_inputs"}:
            continue
        arguments.extend(["--" + field.replace("_", "-"), absolute(value)])
    directory = output / target["id"]
    directory.mkdir(mode=0o700)
    with (directory / "managed-execution.log").open("xb") as log:
        result = subprocess.run(command(target, arguments), stdout=log, stderr=subprocess.STDOUT, check=False)
    if result.returncode not in {0, 1}:
        raise ValueError("guest managed execution failed before retaining an installed campaign")
    archive = directory / "leaf.zip"
    with archive.open("xb") as stream, (directory / "transfer.log").open("xb") as log:
        subprocess.run(command(target, ["python3", "-m", "scripts.linux_release.installed_transfer", "--export", target["output"]]),
                       stdout=stream, stderr=log, check=True)
    leaf = directory / "leaf"
    receive(archive, leaf)
    return leaf


def validate_plan(plan: dict, final: dict) -> tuple:
    evidence.object_fields(plan, {"format_version", "source_commit", "driver_tree", "targets"})
    if type(plan["format_version"]) is not int or plan["format_version"] != 1:
        raise ValueError("unsupported installed guest plan")
    source, tree = evidence.source(plan["source_commit"]), evidence.source(plan["driver_tree"])
    expected = evidence.artifacts(final, source)
    targets = plan["targets"]
    if not isinstance(targets, list) or not targets or len(targets) > 256:
        raise ValueError("installed plan requires bounded explicit guest targets")
    seen = set()
    for target in targets:
        command(target, ["true"])
        if target["id"] in seen or target["artifact_id"] not in expected:
            raise ValueError("guest plan repeats a target or selects an undeclared artifact")
        seen.add(target["id"])
    return source, tree, targets


def execute(plan: dict, final: dict, catalog: dict, output: Path) -> dict:
    source, tree, targets = validate_plan(plan, final)
    output.mkdir(parents=True, mode=0o700, exist_ok=False)
    root = output / "evidence"
    root.mkdir(mode=0o700)
    registrations = {"format_version": 1, "installations": {item["id"]: registry(catalog, item["id"]) for item in catalog["installations"]}}
    (output / "driver-registry.json").write_text(json.dumps(registrations, sort_keys=True, separators=(",", ":")) + "\n")
    rows, row_ids = [], set()
    known = {row["id"]: row for row in expand(catalog)}
    for target in targets:
        leaf = run_guest(target, source, tree, output)
        campaign = read(leaf / "campaign.json")
        # This validation preserves failed/unverified rows while rejecting malformed observations.
        admit(catalog, campaign, final, source, leaf / "evidence")
        for row in campaign["results"]:
            if row["row_id"] in row_ids or row["artifact_id"] != target["artifact_id"]:
                raise ValueError("executed guests repeat a row or changed selected artifact")
            selected = known.get(row["row_id"])
            if selected is None or selected["environment"] != target["environment_id"]:
                raise ValueError("executed guest changed the selected support environment")
            row_ids.add(row["row_id"])
            for case in row["cases"]:
                for record in case["evidence"]:
                    evidence.retained_file(record, leaf / "evidence")
                    record["path"] = target["id"] + "/" + record["path"]
            rows.append(row)
        shutil.copytree(leaf / "evidence", root / target["id"], symlinks=False)
    campaign = {"format_version": 1, "source_commit": source, "artifacts": final["artifacts"], "results": rows}
    report = admit(catalog, campaign, final, source, root)
    for name, value in (("campaign.json", campaign), ("admission.json", report), ("plan.json", plan)):
        (output / name).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    return report


def main(arguments: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plan", type=Path, required=True)
    parser.add_argument("--final-artifacts", type=Path, required=True)
    parser.add_argument("--catalog", type=Path, default=CATALOG)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(arguments)
    try:
        report = execute(read(args.plan), read(args.final_artifacts), read(args.catalog), args.output)
    except (ValueError, OSError, TypeError, subprocess.SubprocessError) as error:
        print("Installed guest execution refused: " + str(error), file=sys.stderr)
        return 2
    return 0 if report["accepted"] else 1

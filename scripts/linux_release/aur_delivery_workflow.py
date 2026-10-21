"""Prepare a separately bound AUR recipe from a trusted controller artifact bundle."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess

from .aur_delivery import plan, production_publish
from .aur_recipe import generate
from .repository_objects import encoded
from .signing import CATALOG


def selected_directory(root: Path, name: str) -> Path:
    if not isinstance(name, str) or Path(name).is_absolute() or Path(name).as_posix() != name or ".." in Path(name).parts:
        raise ValueError("AUR bundle generation path is invalid")
    path = root / name
    if path.is_symlink() or not path.resolve(strict=True).is_relative_to(root.resolve()):
        raise ValueError("AUR bundle generation escapes its selected root")
    return path


def run_identity(value: str) -> int:
    if not isinstance(value, str) or not re.fullmatch(r"[1-9][0-9]*", value):
        raise ValueError("select a numeric controller run")
    return int(value)


def selected_request(source_commit: str, operation: str):
    if not isinstance(source_commit, str) or not re.fullmatch(r"[0-9a-f]{40}", source_commit):
        raise ValueError("select an exact release source commit")
    if operation not in {"prepare", "publish-aur"}:
        raise ValueError("select prepare or the separately authorized publish-aur operation")


def request(run_id: str, source_commit: str, operation: str):
    """Workflow inputs are checked before any controller run is read or downloaded."""
    run_identity(run_id)
    selected_request(source_commit, operation)


def verify_provenance(run: dict, repository_id: int, run_id: int | None = None):
    if (type(repository_id) is not int or type(run.get("repository", {}).get("id")) is not int
            or run["repository"]["id"] != repository_id
            or (run_id is not None and (type(run.get("id")) is not int or run["id"] != run_id))
            or run.get("path") != ".github/workflows/release.yml" or run.get("head_branch") != "main"
            or run.get("event") != "workflow_dispatch" or run.get("status") != "completed"
            or run.get("conclusion") != "success"):
        raise ValueError("AUR inputs must come from the successful protected main release controller")


def prepare(input_root: Path, source_commit: str, receipt: Path, operation: str, *, catalog: Path = CATALOG,
            package="painted-wolf-code"):
    selected_request(source_commit, operation)
    if operation == "publish-aur":
        production_publish()
    root = input_root.resolve(strict=True)
    if receipt.resolve().is_relative_to(root):
        raise ValueError("AUR receipt cannot replace its retained controller inputs")
    selected = json.loads((root / "delivery.json").read_text())
    if (set(selected) != {"format_version", "source_commit", "base_url", "generations"}
            or type(selected["format_version"]) is not int or selected["format_version"] != 1
            or selected["source_commit"] != source_commit or not isinstance(selected["generations"], list)):
        raise ValueError("AUR bundle differs from the selected controller request")
    generations = []
    for name in selected["generations"]:
        directory = selected_directory(root, name)
        manifest = json.loads((directory / "generation.json").read_text())
        if manifest["source_commit"] != source_commit:
            raise ValueError("AUR generation differs from the selected source")
        generations.append((manifest, directory))
    final = root / "final-artifacts.json"
    if final.is_symlink() or not final.is_file():
        raise ValueError("AUR bundle lacks its retained final-artifact inventory")
    receipt.parent.mkdir(parents=True, exist_ok=True)
    recipe = receipt.parent / "recipe"
    metadata = generate(generations, recipe, selected["base_url"], catalog, package=package)
    plan_record = plan(metadata, [row for row, _ in generations], recipe, hashlib.sha256(final.read_bytes()).hexdigest())
    receipt.write_bytes(encoded({**plan_record, "availability": "prepared-only", "aur_published": False}))
    return plan_record


def main(arguments=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    selected = commands.add_parser("request", help="check workflow inputs before reading the controller run")
    provenance = commands.add_parser("provenance", help="admit the downloaded controller run record")
    provenance.add_argument("--run", type=Path, required=True)
    provenance.add_argument("--repository-id", type=int, required=True)
    for command in [selected, provenance]:
        command.add_argument("--release-run-id", required=True)
    prepared = commands.add_parser("prepare", help="prepare the bound recipe from the retained bundle")
    prepared.add_argument("--input-root", type=Path, required=True)
    prepared.add_argument("--receipt", type=Path, required=True)
    for command in [selected, prepared]:
        command.add_argument("--source-commit", required=True)
        command.add_argument("--operation", required=True)
    args = parser.parse_args(arguments)
    try:
        if args.command == "request":
            request(args.release_run_id, args.source_commit, args.operation)
        elif args.command == "provenance":
            verify_provenance(json.loads(args.run.read_text()), args.repository_id, run_identity(args.release_run_id))
        else:
            prepare(args.input_root, args.source_commit, args.receipt, args.operation)
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"AUR delivery preparation refused: {exc}\n")


if __name__ == "__main__":
    main()

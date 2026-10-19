"""Prepare a separately bound AUR recipe from a trusted controller artifact bundle."""

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


def verify_provenance(run: dict, repository_id: int):
    if (type(repository_id) is not int or type(run.get("repository", {}).get("id")) is not int
            or run["repository"]["id"] != repository_id
            or run.get("path") != ".github/workflows/release.yml" or run.get("head_branch") != "main"
            or run.get("event") != "workflow_dispatch" or run.get("status") != "completed"
            or run.get("conclusion") != "success"):
        raise ValueError("AUR inputs must come from the successful protected main release controller")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input-root", type=Path, required=True)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--operation", choices=["prepare", "publish-aur"], required=True)
    args = parser.parse_args()
    try:
        if args.operation == "publish-aur":
            production_publish()
        if not re.fullmatch(r"[0-9a-f]{40}", args.source_commit):
            raise ValueError("select an exact release source commit")
        root = args.input_root.resolve(strict=True)
        if args.receipt.resolve().is_relative_to(root):
            raise ValueError("AUR receipt cannot replace its retained controller inputs")
        request = json.loads((root / "delivery.json").read_text())
        if (set(request) != {"format_version", "source_commit", "base_url", "generations"}
                or type(request["format_version"]) is not int or request["format_version"] != 1
                or request["source_commit"] != args.source_commit or not isinstance(request["generations"], list)):
            raise ValueError("AUR bundle differs from the selected controller request")
        generations = []
        for name in request["generations"]:
            directory = selected_directory(root, name)
            manifest = json.loads((directory / "generation.json").read_text())
            if manifest["source_commit"] != args.source_commit:
                raise ValueError("AUR generation differs from the selected source")
            generations.append((manifest, directory))
        args.receipt.parent.mkdir(parents=True, exist_ok=True)
        recipe = args.receipt.parent / "recipe"
        metadata = generate(generations, recipe, request["base_url"], CATALOG)
        final = root / "final-artifacts.json"
        if final.is_symlink() or not final.is_file():
            raise ValueError("AUR bundle lacks its retained final-artifact inventory")
        selected = plan(metadata, [row for row, _ in generations], recipe, hashlib.sha256(final.read_bytes()).hexdigest())
        args.receipt.write_bytes(encoded({**selected, "availability": "prepared-only", "aur_published": False}))
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"AUR delivery preparation refused: {exc}\n")


if __name__ == "__main__":
    main()

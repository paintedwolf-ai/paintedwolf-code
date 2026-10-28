"""Withdraw a halted release from the AUR by advancing its recipe to a revision that installs nothing."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import shlex
import subprocess

from scripts.release_semver import parse

from .aur_delivery import protected_publisher, recipe_objects
from .aur_recipe import quoted_array
from .repository_objects import encoded

OPERATION = "withdraw-aur"
PACKAGES = {"stable": "painted-wolf-code-bin", "preview": "painted-wolf-code-preview-bin"}
REFUSAL = "Painted Wolf Code {version} was withdrawn after publication. Install a later release."


def srcinfo_fields(raw: bytes) -> dict:
    """Read the published declarations; every key may repeat."""
    fields = {}
    for line in raw.decode("utf-8").splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        key, separator, value = line.strip().partition(" = ")
        if not separator or not re.fullmatch(r"[a-z][a-z0-9_]*", key):
            raise ValueError("published AUR .SRCINFO has an invalid declaration")
        fields.setdefault(key, []).append(value)
    return fields


def single(fields: dict, key: str, pattern: str) -> str:
    values = fields.get(key, [])
    if len(values) != 1 or not re.fullmatch(pattern, values[0]):
        raise ValueError(f"published AUR .SRCINFO lacks one valid {key}")
    return values[0]


def recipe(previous: dict, srcinfo: bytes, destination: Path, withdrawn_commit: str) -> dict:
    """Write the refusing revision: the same name, version and epoch, the next pkgrel and no sources."""
    fields = srcinfo_fields(srcinfo)
    package = single(fields, "pkgbase", r"painted-wolf-code(?:-preview)?-bin")
    if package != previous.get("package") or fields.get("pkgname") != [package]:
        raise ValueError("published AUR recipe is not the halted release's package")
    version = single(fields, "pkgver", r"[0-9A-Za-z.+_]+")
    release = int(single(fields, "pkgrel", r"[1-9][0-9]*(?:\.[0-9]+)?").split(".")[0]) + 1
    epoch = single(fields, "epoch", r"[0-9]+") if "epoch" in fields else None
    architectures = sorted(set(fields.get("arch", [])))
    if not architectures or any(not re.fullmatch(r"[a-z0-9_]+", value) for value in architectures):
        raise ValueError("published AUR recipe declares no valid architecture")
    licenses = fields.get("license", [])
    scalars = [("pkgver", version), ("pkgrel", str(release)), *([("epoch", epoch)] if epoch is not None else []),
               ("pkgdesc", "Painted Wolf Code (withdrawn release)"), ("url", "https://paintedwolf.ai")]
    text = "# Withdrawn by the release halt; this revision installs nothing.\n"
    text += "".join(f"{key}={shlex.quote(value)}\n" for key, value in [("pkgname", package), *scalars])
    text += quoted_array("arch", architectures) + quoted_array("license", licenses)
    text += quoted_array("options", ["!strip", "!debug"])
    text += "package() {\n  error " + shlex.quote(REFUSAL.format(version=previous["version"])) + "\n  return 1\n}\n"
    # The same order makepkg --printsrcinfo prints, so the declarations read back identically.
    rows = [("pkgdesc", scalars[-2][1]), *scalars[:-2], ("url", scalars[-1][1]),
            *(("arch", value) for value in architectures), *(("license", value) for value in licenses),
            ("options", "!strip"), ("options", "!debug")]
    declarations = "pkgbase = " + package + "\n" + "".join(f"\t{key} = {value}\n" for key, value in rows)
    destination.mkdir(parents=True, exist_ok=False)
    (destination / "PKGBUILD").write_text(text)
    (destination / ".SRCINFO").write_text(declarations + "\npkgname = " + package + "\n")
    objects = recipe_objects(destination)
    return {"format_version": 1, "operation": OPERATION,
            **{key: previous[key] for key in ["source_commit", "version", "channel", "package", "rehearsal"]},
            "withdrawn_commit": withdrawn_commit, "pkgrel": release,
            "recipe_objects": objects, "recipe_sha256": hashlib.sha256(encoded(objects)).hexdigest()}


def authority(bad: str, channel: str, package: str):
    """The halt's selected version and channel are the only authority a withdrawal carries."""
    def authorize(selected, recipe_dir):
        if (selected.get("operation") != OPERATION or selected.get("version") != bad
                or selected.get("channel") != channel or selected.get("package") != package):
            raise ValueError("AUR withdrawal differs from the halted release")
        objects = recipe_objects(recipe_dir)
        if (objects != selected.get("recipe_objects")
                or hashlib.sha256(encoded(objects)).hexdigest() != selected.get("recipe_sha256")):
            raise ValueError("AUR withdrawal recipe differs from its prepared revision")
    return authorize


def withdraw(bad: str, work: Path, private_key: Path, known_hosts: Path, *, dry_run=False,
             publisher_factory=None) -> dict:
    """Advance each channel recipe that offers the halted version to its refusing revision."""
    parse(bad)
    factory = publisher_factory or protected_publisher
    results = {}
    for channel, package in PACKAGES.items():
        root = work / channel
        root.mkdir(parents=True)
        publisher = factory(root, package, private_key, known_hosts, authority(bad, channel, package))
        current = publisher.inspect()
        if current is None:
            results[channel] = {"package": package, "availability": "no-recipe", "withdrawn": False}
            continue
        record = current["record"]
        if record.get("package") != package:
            raise ValueError("AUR origin offers a foreign package record")
        if record.get("version") != bad:
            results[channel] = {"package": package, "availability": "other-version", "withdrawn": False,
                                "offered": record.get("version")}
            continue
        if record.get("operation") == OPERATION:
            results[channel] = {**record, "commit": current["commit"], "availability": "origin-readback",
                                "withdrawn": True}
            continue
        selected = recipe(record, current["srcinfo"], root / "recipe", current["commit"])
        if dry_run:
            results[channel] = {**selected, "availability": "prepared-only", "withdrawn": False}
            continue
        results[channel] = {**publisher.withdraw(selected, root / "recipe", current["commit"]), "withdrawn": True}
    return results


def main(arguments=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bad", required=True)
    parser.add_argument("--private-key", type=Path, required=True)
    parser.add_argument("--known-hosts", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args(arguments)
    try:
        args.output.mkdir(parents=True, exist_ok=True)
        result = withdraw(args.bad, args.output, args.private_key, args.known_hosts, dry_run=args.dry_run)
        (args.output / "withdrawal.json").write_bytes(encoded(result))
        print(json.dumps(result, indent=2, sort_keys=True))
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"AUR withdrawal refused: {exc}\n")


if __name__ == "__main__":
    main()

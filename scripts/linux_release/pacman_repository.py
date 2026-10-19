"""Construct an immutable signed pacman generation without publishing indexes."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

from .openpgp import keyring, run, selection
from .signing import CATALOG, digest, sign, verify

NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9+._~-]*\Z")
ARCHITECTURES = {"x86_64", "aarch64"}


def package_info(path: Path) -> dict[str, str]:
    raw = subprocess.check_output(["bsdtar", "-xOf", str(path), ".PKGINFO"], timeout=60, text=True)
    selected = {}
    for line in raw.splitlines():
        if line.startswith("#") or not line.strip():
            continue
        key, separator, value = line.partition(" = ")
        if not separator:
            raise ValueError("invalid native PKGINFO field framing")
        if key in {"pkgname", "pkgver", "arch"}:
            if key in selected or not value:
                raise ValueError("ambiguous native package identity")
            selected[key] = value
    if selected.keys() != {"pkgname", "pkgver", "arch"}:
        raise ValueError("native package is missing identity fields")
    return selected


def binary_signature(subject: Path, destination: Path, catalog: Path, private: Path, rehearsal: bool):
    selected, public = selection(catalog, rehearsal)
    with tempfile.TemporaryDirectory(prefix="paintedwolf-pacman-signature-") as directory:
        armored = Path(directory) / "signature.asc"
        sign("detached", subject, armored, catalog, private, rehearsal)
        with keyring(public, selected["fingerprint"]) as home:
            run(["gpg", "--batch", "--yes", "--dearmor", "--output", str(destination.resolve()), str(armored)], home)
        verify("detached", subject, destination, catalog, rehearsal)


def assemble(manifest: dict, inputs: Path, destination: Path, channel: str,
             catalog: Path, private: Path, rehearsal=False, package="painted-wolf-code") -> dict:
    if channel not in {"stable", "preview"} or manifest.get("format_version") != 1:
        raise ValueError("unsupported pacman generation or channel")
    source = manifest.get("source_commit", "")
    architecture = manifest.get("architecture")
    if not re.fullmatch(r"[0-9a-f]{40}", source) or architecture not in ARCHITECTURES:
        raise ValueError("pacman generation requires an exact source and architecture")
    version = manifest.get("version", "")
    if not NAME.fullmatch(version):
        raise ValueError("invalid product version")
    rows = manifest.get("packages")
    if not isinstance(rows, list) or not rows or len({row["file"] for row in rows}) != len(rows):
        raise ValueError("pacman generation requires distinct selected packages")
    selected_inputs = []
    for row in rows:
        name = row["file"]
        if not NAME.fullmatch(name) or not name.endswith(".pkg.tar.zst"):
            raise ValueError("pacman input must be a sibling native package file")
        path = inputs / name
        if path.is_symlink() or not path.is_file() or digest(path) != row["sha256"]:
            raise ValueError("pacman package differs from its selected immutable input")
        info = package_info(path)
        if info != {"pkgname": package, "pkgver": row["native_version"], "arch": architecture}:
            raise ValueError("native pacman identity differs from its selected receipt")
        selected_inputs.append(path)
    selection(catalog, rehearsal)
    destination.mkdir(parents=True, exist_ok=False)
    packages = []
    for path in selected_inputs:
        target = destination / path.name
        shutil.copyfile(path, target)
        binary_signature(target, target.with_name(target.name + ".sig"), catalog, private, rehearsal)
        packages.append(target)
    repository = "painted-wolf-code" + ("-preview" if channel == "preview" else "")
    database = destination / (repository + ".db.tar.gz")
    subprocess.run(["repo-add", "--nocolor", "--include-sigs", str(database),
                    *map(str, packages)], check=True)
    for suffix in ["db", "files"]:
        archive = destination / f"{repository}.{suffix}.tar.gz"
        signature = archive.with_name(archive.name + ".sig")
        binary_signature(archive, signature, catalog, private, rehearsal)
        alias = destination / f"{repository}.{suffix}.sig"
        alias.symlink_to(signature.name)
    objects = []
    for path in sorted(destination.iterdir()):
        if path.is_symlink() and path.resolve().parent != destination.resolve():
            raise ValueError("repository alias escapes its immutable generation")
        if path.is_file():
            objects.append({"path": path.name, "sha256": digest(path), "size": path.stat().st_size})
    return {"format_version": 1, "source_commit": source, "version": version,
            "architecture": architecture, "channel": channel, "repository": repository,
            "rehearsal": rehearsal, "activation": "not-published", "objects": objects,
            "reader_consistency": "immutable-generation-config", "aur_published": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ["manifest", "inputs", "destination", "catalog", "private-key"]:
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--channel", choices=["stable", "preview"], required=True)
    parser.add_argument("--rehearsal", action="store_true")
    args = parser.parse_args()
    try:
        result = assemble(json.loads(args.manifest.read_text()), args.inputs, args.destination,
                          args.channel, args.catalog, args.private_key, args.rehearsal)
        (args.destination / "generation.json").write_text(json.dumps(result, indent=2) + "\n")
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Pacman repository assembly failed: {exc}\n")


if __name__ == "__main__":
    main()

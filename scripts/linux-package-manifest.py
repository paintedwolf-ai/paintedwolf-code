#!/usr/bin/env python3
"""Write or verify the Linux package manifest of executable digests.

`generate` hashes every executable the package installs, as the build staged
it, and writes `manifest.json` (shape: packaging/linux/manifest/manifest.schema.json).
`verify-tree` reads the manifest inside an extracted package tree and checks
that every listed executable is present with the recorded digest and size, so
staging proves the shipped manifest describes the shipped bytes.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import sys

FORMAT_VERSION = 1
PACKAGE_NAME = "painted-wolf-code"
INSTALL_ROOT = "/opt/painted-wolf-code"
SHELL_EXECUTABLE = "/usr/bin/painted-wolf-code"
HELPERS = ("pw", "pw-logs", "pw-document-core", "bialy")
OPTIONAL_HELPERS = ("pw-vault-identity",)
ENTRY_POINTS = ("pw", "pw-logs")
ARCHITECTURES = {"x86_64-unknown-linux-gnu": "x86_64", "aarch64-unknown-linux-gnu": "aarch64"}
HEX_DIGEST = re.compile(r"^[0-9a-f]{64}$")


def digest(path: Path) -> tuple[str, int]:
    hasher = hashlib.sha256()
    size = 0
    with path.open("rb") as handle:
        while chunk := handle.read(1 << 20):
            hasher.update(chunk)
            size += len(chunk)
    return hasher.hexdigest(), size


def is_executable_file(path: Path) -> bool:
    info = path.lstat()
    return stat.S_ISREG(info.st_mode) and bool(info.st_mode & 0o111)


def entry(installed: str, source: Path) -> dict:
    if not is_executable_file(source):
        raise ValueError(f"{source} for {installed} is not an executable regular file")
    sha, size = digest(source)
    return {"path": installed, "sha256": sha, "size": size}


def generate(args: argparse.Namespace) -> int:
    root: Path = args.root.resolve()
    tauri_dir = root / "lycaon-den" / "src-tauri"
    architecture = ARCHITECTURES.get(args.target)
    if architecture is None:
        raise ValueError(f"unsupported Linux target {args.target}")
    executables = [entry(SHELL_EXECUTABLE, tauri_dir / "target" / args.target / "release" / PACKAGE_NAME)]
    for helper in HELPERS:
        executables.append(entry(f"{INSTALL_ROOT}/bin/{helper}", tauri_dir / "binaries" / f"{helper}-{args.target}"))
    for helper in OPTIONAL_HELPERS:
        source = tauri_dir / "binaries" / f"{helper}-{args.target}"
        if source.exists():
            executables.append(entry(f"{INSTALL_ROOT}/bin/{helper}", source))
    for name in ENTRY_POINTS:
        executables.append(entry(f"/usr/bin/{name}", root / "packaging" / "linux" / "bin" / name))
    engine_root = tauri_dir / "engine-root"
    for dirpath, dirnames, filenames in os.walk(engine_root):
        dirnames.sort()
        for filename in sorted(filenames):
            source = Path(dirpath) / filename
            if source.is_symlink() or not is_executable_file(source):
                continue
            relative = source.relative_to(engine_root).as_posix()
            executables.append(entry(f"{INSTALL_ROOT}/engine-root/{relative}", source))
    manifest = {
        "format_version": FORMAT_VERSION,
        "package": PACKAGE_NAME,
        "version": args.version,
        "architecture": architecture,
        "install_root": INSTALL_ROOT,
        "executables": sorted(executables, key=lambda row: row["path"]),
    }
    validate(manifest)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print(f"linux-package-manifest: {len(executables)} executables recorded in {args.output}", file=sys.stderr)
    return 0


def validate(manifest: dict) -> None:
    expected_keys = {"format_version", "package", "version", "architecture", "install_root", "executables"}
    if set(manifest) != expected_keys:
        raise ValueError("manifest keys do not match the schema")
    if manifest["format_version"] != FORMAT_VERSION or manifest["package"] != PACKAGE_NAME:
        raise ValueError("manifest identity does not match this package")
    if manifest["install_root"] != INSTALL_ROOT or manifest["architecture"] not in ARCHITECTURES.values():
        raise ValueError("manifest install root or architecture is unsupported")
    if not isinstance(manifest["version"], str) or not manifest["version"]:
        raise ValueError("manifest version is empty")
    rows = manifest["executables"]
    if not isinstance(rows, list) or not rows:
        raise ValueError("manifest lists no executables")
    seen: set[str] = set()
    for row in rows:
        if set(row) != {"path", "sha256", "size"}:
            raise ValueError(f"manifest row has unexpected keys: {row}")
        if not row["path"].startswith("/") or row["path"] in seen:
            raise ValueError(f"manifest path is relative or repeated: {row['path']}")
        if not HEX_DIGEST.match(row["sha256"]) or not isinstance(row["size"], int) or row["size"] < 0:
            raise ValueError(f"manifest digest or size is malformed for {row['path']}")
        seen.add(row["path"])
    for required in (SHELL_EXECUTABLE, *(f"{INSTALL_ROOT}/bin/{helper}" for helper in HELPERS), *(f"/usr/bin/{name}" for name in ENTRY_POINTS)):
        if required not in seen:
            raise ValueError(f"manifest omits {required}")


def verify_tree(args: argparse.Namespace) -> int:
    tree: Path = args.tree.resolve()
    manifest_path = tree / INSTALL_ROOT.lstrip("/") / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    validate(manifest)
    if args.version is not None and manifest["version"] != args.version:
        raise ValueError(f"manifest version {manifest['version']} does not equal {args.version}")
    if args.architecture is not None and manifest["architecture"] != args.architecture:
        raise ValueError(f"manifest architecture {manifest['architecture']} does not equal {args.architecture}")
    listed = {row["path"] for row in manifest["executables"]}
    for row in manifest["executables"]:
        installed = tree / row["path"].lstrip("/")
        if not is_executable_file(installed):
            raise ValueError(f"manifest lists {row['path']} but the package has no executable there")
        sha, size = digest(installed)
        if sha != row["sha256"] or size != row["size"]:
            raise ValueError(f"manifest digest mismatch for {row['path']}")
    # Every executable the package installs under its roots must be listed.
    for base in (tree / INSTALL_ROOT.lstrip("/"), tree / "usr" / "bin"):
        for dirpath, _, filenames in os.walk(base):
            for filename in filenames:
                path = Path(dirpath) / filename
                if path.is_symlink() or not is_executable_file(path):
                    continue
                installed = "/" + path.relative_to(tree).as_posix()
                if installed not in listed:
                    raise ValueError(f"package installs executable {installed} that the manifest omits")
    print(f"linux-package-manifest: {len(listed)} executables verified under {tree}", file=sys.stderr)
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)
    gen = commands.add_parser("generate")
    gen.add_argument("--root", type=Path, default=Path(__file__).resolve().parent.parent)
    gen.add_argument("--target", required=True)
    gen.add_argument("--version", required=True)
    gen.add_argument("--output", type=Path, required=True)
    gen.set_defaults(run=generate)
    verify = commands.add_parser("verify-tree")
    verify.add_argument("--tree", type=Path, required=True, help="extracted package root")
    verify.add_argument("--version")
    verify.add_argument("--architecture", choices=sorted(ARCHITECTURES.values()))
    verify.set_defaults(run=verify_tree)
    args = parser.parse_args()
    try:
        return args.run(args)
    except (OSError, ValueError, KeyError, TypeError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())

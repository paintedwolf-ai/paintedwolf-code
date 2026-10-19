"""Seal final Nix application bytes without making a publisher trust claim."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import stat
import struct

ROLES = ("painted-wolf-code", "pw", "pw-logs", "pw-vault-identity", "pw-document-core", "bialy")
MACHINES = {"x86_64": 62, "aarch64": 183}


def entry(path: Path) -> dict:
    digest = hashlib.sha256()
    size = 0
    with path.open("rb") as source:
        while chunk := source.read(1 << 20):
            digest.update(chunk)
            size += len(chunk)
    return {"path": str(path), "sha256": digest.hexdigest(), "size": size}


def elf_architecture(path: Path, architecture: str, required=False):
    with path.open("rb") as source:
        header = source.read(20)
    if not header.startswith(b"\x7fELF"):
        if required:
            raise ValueError(f"application role must remain a real ELF: {path}")
        return
    if (len(header) != 20 or header[4:7] != b"\x02\x01\x01"
            or struct.unpack_from("<H", header, 18)[0] != MACHINES[architecture]):
        raise ValueError(f"ELF differs from the selected native architecture: {path}")


def seal(output: Path, architecture: str, version: str, nix_store: Path) -> dict:
    if architecture not in MACHINES or not version or version != version.strip():
        raise ValueError("select a supported architecture and complete product version")
    if not output.is_absolute() or output.resolve() != output:
        raise ValueError("output must identify the real absolute Nix generation")
    if not nix_store.is_absolute() or nix_store.name != "nix-store":
        raise ValueError("layout requires the selected absolute Nix GC root registrar")
    root = output / "lib/painted-wolf-code"
    for role in ROLES:
        path = root / "bin" / role
        if path.is_symlink() or not path.is_file() or not path.stat().st_mode & 0o111:
            raise ValueError(f"application role must be an installed executable regular file: {role}")
        elf_architecture(path, architecture, required=True)
    for required in ["release-version", "THIRD-PARTY-NOTICES.md", "engine-root"]:
        if not (root / required).exists():
            raise ValueError(f"incomplete application resources: {required}")
    if (root / "release-version").read_text().strip() != version:
        raise ValueError("resource and application product versions differ")
    if not (root / "THIRD-PARTY-NOTICES.md").is_file() or not (root / "THIRD-PARTY-NOTICES.md").stat().st_size:
        raise ValueError("complete dependency notices are required")
    executables = []
    for path in sorted(root.rglob("*")):
        if path.is_symlink() or not path.is_file():
            continue
        mode = path.stat().st_mode
        if mode & (stat.S_ISUID | stat.S_ISGID):
            raise ValueError(f"Nix application payload must not install privileged executable modes: {path}")
        elf_architecture(path, architecture)
        if mode & 0o111:
            executables.append(entry(path))
    manifest = {"format_version": 1, "package": "painted-wolf-code", "version": version,
                "architecture": architecture, "install_root": str(root), "executables": executables}
    receipt = {"format_version": 1, "output": str(output), "nix_store": str(nix_store)}
    (root / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    (root / "nix-layout.json").write_text(json.dumps(receipt, indent=2) + "\n")
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--architecture", choices=MACHINES, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--nix-store", type=Path, required=True)
    args = parser.parse_args()
    try:
        seal(args.output, args.architecture, args.version, args.nix_store)
    except (OSError, ValueError) as exc:
        parser.exit(1, f"Final Nix application manifest refused: {exc}\n")


if __name__ == "__main__":
    main()

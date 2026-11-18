#!/usr/bin/env python3
"""Hold a staged updater artifact to the client's download and expansion limits."""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import tarfile

LIMITS = Path(__file__).resolve().parent.parent / "packaging/update-limits.json"


def inside_bundle(path: str, link: str) -> bool:
    if link.startswith("/"):
        return False
    depth = len([part for part in path.split("/")[:-1] if part])
    for part in link.split("/"):
        if part == "..":
            if depth == 0:
                return False
            depth -= 1
        elif part not in ("", "."):
            depth += 1
    return True


def check(artifact: Path, version: str, platform: str, limits_path: Path = LIMITS) -> None:
    limits = json.loads(limits_path.read_text())
    if limits["format_version"] != 1:
        raise ValueError("unsupported updater limits")
    if artifact.stat().st_size > limits["max_download_bytes"]:
        raise ValueError("updater artifact exceeds the client download limit")
    if not artifact.name.endswith(".tar.gz"):
        return
    size = 0
    seen = set()
    root = None
    product_versions = []
    with tarfile.open(artifact, "r|gz") as archive:
        for member in archive:
            parts = [part for part in member.name.split("/") if part]
            if not parts or member.name.startswith("/") or ".." in parts or "." in parts:
                raise ValueError(f"updater archive entry escapes the bundle: {member.name}")
            if root is None:
                root = parts[0]
                if not root.endswith(".app"):
                    raise ValueError("updater archive root must be one application bundle")
            if parts[0] != root:
                raise ValueError("updater archive has more than one root")
            relative = "/".join(parts[1:])
            if relative:
                if relative in seen:
                    raise ValueError(f"updater archive repeats {relative}")
                seen.add(relative)
            if member.issym():
                if not inside_bundle(relative, member.linkname):
                    raise ValueError(f"updater archive link leaves the bundle: {member.name}")
            elif not (member.isfile() or member.isdir()):
                raise ValueError(f"updater archive has an unsupported entry type: {member.name}")
            if relative == "Contents/Resources/release-version":
                if not member.isfile() or member.size > 128:
                    raise ValueError("invalid updater product version resource")
                product_versions.append(archive.extractfile(member).read().decode().strip())
            size += member.size
            if size > limits["max_expanded_bytes"] or len(seen) > limits["max_archive_entries"]:
                raise ValueError("updater artifact exceeds the client expansion limit")
    if platform.startswith("darwin-") and product_versions != [version]:
        raise ValueError("updater bundle product version does not match the release")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifact", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--platform", required=True)
    args = parser.parse_args()
    try:
        check(args.artifact, args.version, args.platform)
    except (OSError, ValueError, tarfile.TarError) as error:
        parser.exit(1, f"error: {error}\n")


if __name__ == "__main__":
    main()

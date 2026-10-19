"""Bind nonpublishing native candidate family coverage to the retained package bytes."""

import argparse
import hashlib
import json
from pathlib import Path
import re


def inventory(directory, platform, architecture, version, source_commit):
    if architecture not in {"x86_64", "aarch64"} or not re.fullmatch(r"[a-f0-9]{40}", source_commit):
        raise ValueError("candidate inventory requires its exact source and native architecture")
    stem = f"painted-wolf-code_v{version}_{platform}"
    packages = []
    for family, extension in [("deb", "deb"), ("rpm", "rpm"), ("pacman", "pkg.tar.zst")]:
        path = directory / (stem + "." + extension)
        if path.is_symlink() or not path.is_file():
            raise ValueError("candidate inventory requires every native package family")
        with path.open("rb") as source:
            digest = hashlib.file_digest(source, "sha256").hexdigest()
        packages.append({"family": family, "file": path.name, "architecture": architecture,
                         "sha256": digest, "bytes": path.stat().st_size})
    return {"format_version": 1, "source_commit": source_commit, "version": version,
            "platform": platform, "architecture": architecture, "packages": packages,
            "qualification_status": "unverified", "published": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path, required=True)
    for name in ["platform", "architecture", "version", "source-commit"]:
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    result = inventory(args.directory, args.platform, args.architecture, args.version, args.source_commit)
    with (args.directory / "candidate-packages.json").open("x") as output:
        output.write(json.dumps(result, sort_keys=True, indent=2) + "\n")


if __name__ == "__main__":
    main()

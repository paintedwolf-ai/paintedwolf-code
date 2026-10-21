"""Refuse first-party package executables that still carry the builder's paths.

Release builds remap the builder's home, Rust toolchain, Cargo home and checkout
(`scripts/rust-path-remap.sh`). A binary that still contains one of them exposes
the build host's layout and shows that a build step bypassed the remap.
"""

from __future__ import annotations

import argparse
import os
from pathlib import Path
import sys

INSTALL_ROOT = "opt/painted-wolf-code"
FIRST_PARTY = ("painted-wolf-code", "pw", "pw-logs", "pw-document-core", "bialy", "pw-vault-identity")
CHUNK = 1 << 20


def builder_paths(environment, source):
    """The absolute directories a release build must not embed."""
    home = environment.get("HOME", "").rstrip("/")
    candidates = [home, environment.get("CARGO_HOME") or (home and home + "/.cargo"),
                  environment.get("RUSTUP_HOME") or (home and home + "/.rustup"), str(source)]
    paths = []
    for candidate in candidates:
        candidate = candidate.rstrip("/")
        if candidate.startswith("/") and len(candidate) > 1 and candidate not in paths:
            paths.append(candidate)
    if not paths:
        raise ValueError("no builder path is known to check")
    return paths


def embedded(path, needles):
    """Needles found in a file, read in bounded chunks with overlap across boundaries."""
    encoded = [needle.encode() for needle in needles]
    overlap = max(len(needle) for needle in encoded) - 1
    found, tail = set(), b""
    with path.open("rb") as stream:
        while chunk := stream.read(CHUNK):
            window = tail + chunk
            found.update(needle for needle, raw in zip(needles, encoded) if raw in window)
            tail = window[-overlap:] if overlap else b""
    return sorted(found)


def check(tree, needles):
    binaries = Path(tree) / INSTALL_ROOT / "bin"
    present = [binaries / name for name in FIRST_PARTY if (binaries / name).is_file()]
    if not present:
        raise ValueError(f"{binaries} contains no first-party executable to check")
    leaks = {path.name: hits for path in present if (hits := embedded(path, needles))}
    if leaks:
        detail = "; ".join(f"{name}: {', '.join(hits)}" for name, hits in sorted(leaks.items()))
        raise ValueError("first-party executables embed builder paths: " + detail)
    return [path.name for path in present]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tree", type=Path, required=True)
    parser.add_argument("--source", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        checked = check(args.tree, builder_paths(os.environ, args.source.resolve()))
    except (OSError, ValueError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    print("build paths: no builder path in " + ", ".join(checked))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

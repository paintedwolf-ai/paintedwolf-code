"""Exact-path AppArmor user-namespace grants that a Nix generation ships for its own executables.

Hosts that restrict unprivileged user namespaces to profiled programs (Ubuntu 24.04 and
later) need one before that executable can build a sandbox; an administrator loads it.
"""

from __future__ import annotations

import argparse
import hashlib
from pathlib import Path

from .apparmor_attachment import literal

ROLES = {"engine": "paintedwolf-engine-", "scanner-builder": "paintedwolf-scanner-builder-"}


def profile(role: str, executable: Path) -> tuple[str, str]:
    """The profile name and text granting user namespaces to exactly `executable`."""
    if role not in ROLES or not executable.is_absolute():
        raise ValueError("a grant names a known role and an absolute executable")
    name = ROLES[role] + hashlib.sha256(str(executable).encode()).hexdigest()
    return name, ("abi <abi/4.0>,\n\n"
                  f"profile {name} {literal(str(executable))} flags=(unconfined) {{\n"
                  "  userns,\n}\n")


def write(role: str, executable: Path, output: Path) -> Path:
    name, text = profile(role, executable)
    directory = output / "share/painted-wolf-code/apparmor"
    directory.mkdir(parents=True, exist_ok=True)
    (directory / name).write_text(text)
    return directory / name


def main(arguments=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("role", choices=sorted(ROLES))
    parser.add_argument("executable", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args(arguments)
    if not args.executable.is_file() or not args.executable.is_relative_to(args.output):
        parser.exit(1, "the granted executable must belong to the output that ships the grant\n")
    print(write(args.role, args.executable, args.output))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

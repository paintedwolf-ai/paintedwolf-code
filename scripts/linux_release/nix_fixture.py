"""Build the locked Nix workflow fixture and expose its store tools to the confined Nix suites."""

from __future__ import annotations

import json
import os
from pathlib import Path
import platform
import re
import shlex
import subprocess
import sys

from .nix import CREDENTIALS, NIX, ROOT, SYSTEMS

FIELDS = ("bash", "coreutils", "stdenv")
STORE_TOOL = re.compile(r"/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[^\s]+\Z")


def environment_from(fixture: dict) -> dict:
    if fixture.get("schema") != 1 or set(fixture) != {"schema", *FIELDS}:
        raise ValueError("invalid locked Nix workflow fixture")
    values = {}
    for field in FIELDS:
        value = fixture[field]
        if not isinstance(value, str) or not STORE_TOOL.fullmatch(value):
            raise ValueError("invalid locked Nix workflow path")
        values["PW_NIX_FIXTURE_" + field.upper()] = value
    return values


def build(root: Path = ROOT, run=subprocess.run) -> dict:
    """Realize the fixture from this checkout's lock and return the suites' environment."""
    if platform.system() != "Linux" or platform.machine() not in SYSTEMS:
        raise ValueError("the Nix workflow fixture builds on native Linux amd64 or arm64")
    target = f"git+file://{root}#packages.{SYSTEMS[platform.machine()]}.nix-workflow-fixture"
    environment = {name: value for name, value in os.environ.items() if name not in CREDENTIALS}
    result = run(NIX + ["build", "--json", "--no-link", "--no-write-lock-file", target],
                 cwd=root, env=environment, text=True, stdout=subprocess.PIPE)
    if result.returncode != 0:
        raise ValueError("the locked Nix workflow fixture did not build")
    outputs = [path for build in json.loads(result.stdout) for path in build["outputs"].values()]
    if len(outputs) != 1:
        raise ValueError("the Nix workflow fixture has exactly one output")
    return environment_from(json.loads(Path(outputs[0]).read_text()))


def main() -> int:
    try:
        values = build()
    except (OSError, ValueError, KeyError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    for name, value in values.items():
        print(f"export {name}={shlex.quote(value)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

#!/usr/bin/env python3
"""Copy only catalog-public platform bytes into the publication boundary."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import shutil
import sys

from release_semver import parse


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--catalog", type=Path, required=True)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    try:
        version = args.version
        parse(version)
        catalog = json.loads(args.catalog.read_text(encoding="utf-8"))
        rows = catalog["platforms"]
        if not isinstance(rows, list) or any(
            not isinstance(row, dict)
            or row.get("publication") not in {"public", "candidate"}
            for row in rows
        ):
            raise ValueError("release platform catalog has an invalid publication state")
        public = [row for row in rows if row.get("publication") == "public"]
        if catalog.get("schema_version") != 1 or not public:
            raise ValueError("release platform catalog has no public platform")
        if args.output.exists() and any(args.output.iterdir()):
            raise ValueError("public release artifact output must be empty")
        args.output.mkdir(parents=True, exist_ok=True)
        copied: set[str] = set()
        for row in public:
            stem = f"painted-wolf-code_v{version}_{row['updater_key']}"
            names = (
                f"{stem}.{row['package_extension']}",
                f"{stem}.{row['package_extension']}.sha256",
                f"{stem}.{row['updater_extension']}",
                f"{stem}.{row['updater_extension']}.sig",
            )
            for name in names:
                source = args.input / name
                if not source.is_file() or source.is_symlink():
                    raise ValueError(f"public release artifact missing: {name}")
                if name in copied:
                    raise ValueError(f"public release artifact repeated: {name}")
                shutil.copyfile(source, args.output / name)
                copied.add(name)
            audit = args.input / "opengrep" / f"{stem}.json"
            if not audit.is_file() or audit.is_symlink():
                raise ValueError(f"public release OpenGrep audit missing: {stem}")
            audit_name = f"{stem}.opengrep.json"
            shutil.copyfile(audit, args.output / audit_name)
            copied.add(audit_name)
        if not copied:
            raise ValueError("public release artifact set is empty")
    except (KeyError, OSError, TypeError, ValueError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

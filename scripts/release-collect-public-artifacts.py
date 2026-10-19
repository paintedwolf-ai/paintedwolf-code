#!/usr/bin/env python3
"""Copy only catalog-public platform bytes into the publication boundary."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import shutil
import sys

from release_semver import parse
from release_distribution_contract.platforms import load_platforms


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
        public = [row for row in load_platforms(args.catalog).values() if row["publication"] == "public"]
        if args.output.exists() and any(args.output.iterdir()):
            raise ValueError("public release artifact output must be empty")
        args.output.mkdir(parents=True, exist_ok=True)
        copied: set[str] = set()
        for row in public:
            stem = f"painted-wolf-code_v{version}_{row['updater_key']}"
            extensions = row["package_extensions"]
            if not isinstance(extensions, list) or not extensions:
                raise ValueError(f"public release platform {row['updater_key']} lists no package")
            names = [f"{stem}.{extension}{suffix}" for extension in extensions for suffix in ("", ".sha256")]
            updater = row.get("updater_extension")
            if updater is not None:
                names += [f"{stem}.{updater}", f"{stem}.{updater}.sig"]
            for name in dict.fromkeys(names):
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

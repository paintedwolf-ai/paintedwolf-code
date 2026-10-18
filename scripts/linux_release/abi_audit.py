"""Audit every packaged ELF's imported symbol versions against the shared floor."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import sys

from .elf import inspect

MACHINES = {"x86_64": 62, "aarch64": 183}
NAMESPACES = {"GLIBC": "glibc", "GLIBCXX": "glibcxx", "CXXABI": "cxxabi"}
VERSION = re.compile(r"[0-9]+(?:\.[0-9]+)*\Z")


def version(value: str) -> tuple[int, ...]:
    if not VERSION.fullmatch(value):
        raise ValueError(f"invalid ABI version {value!r}")
    return tuple(int(part) for part in value.split("."))


def violations(facts: dict, floors: dict[str, str]) -> list[str]:
    errors = []
    for library, requirements in facts["versions"].items():
        for requirement in requirements:
            namespace, separator, number = requirement.partition("_")
            if not separator or namespace not in NAMESPACES:
                continue
            floor = floors[NAMESPACES[namespace]]
            if requirement == "GLIBC_ABI_DT_RELR":
                number = "2.36"
            if not VERSION.fullmatch(number):
                errors.append(f"{library} imports unsupported {requirement}")
            elif version(number) > version(floor):
                errors.append(f"{library} imports {requirement} above {namespace}_{floor}")
    return errors


def audit(tree: Path, architecture: str, floors: dict[str, str]) -> dict:
    for field in NAMESPACES.values():
        version(floors[field])
    tree = tree.resolve(strict=True)
    objects, errors = [], []
    for path in sorted(tree.rglob("*")):
        if path.is_symlink() or not path.is_file():
            continue
        relative = path.relative_to(tree).as_posix()
        try:
            facts = inspect(path)
            if facts is None:
                continue
            if facts["machine"] != MACHINES[architecture]:
                errors.append(f"{relative}: wrong ELF machine {facts['machine']}")
            errors.extend(f"{relative}: {error}" for error in violations(facts, floors))
            with path.open("rb") as source:
                hasher = hashlib.sha256()
                while chunk := source.read(1 << 20):
                    hasher.update(chunk)
                digest = hasher.hexdigest()
            objects.append({"path": relative, "sha256": digest, **facts})
        except (OSError, ValueError) as exc:
            errors.append(f"{relative}: {exc}")
    if not objects:
        errors.append("package tree contains no audited ELF objects")
    return {"format_version": 1, "architecture": architecture, "floors": floors,
            "objects": objects, "errors": errors, "passed": not errors}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tree", type=Path, required=True)
    parser.add_argument("--architecture", choices=MACHINES, required=True)
    parser.add_argument("--floors", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        floors = dict(line.split() for line in args.floors.read_text().splitlines() if line.strip())
        result = audit(args.tree, args.architecture, floors)
        args.output.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    except (OSError, ValueError, KeyError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    for error in result["errors"]:
        print(f"error: {error}", file=sys.stderr)
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())

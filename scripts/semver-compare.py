#!/usr/bin/env python3
"""Strict SemVer ordering for release, halt, and recovery guards."""

import sys

from release_semver import compare, parse


def main() -> int:
    if len(sys.argv) != 4 or sys.argv[1] not in {"gt", "ge", "eq", "lt", "le"}:
        print("usage: semver-compare.py {gt|ge|eq|lt|le} LEFT RIGHT", file=sys.stderr)
        return 2
    try:
        result = compare(parse(sys.argv[2]), parse(sys.argv[3]))
    except ValueError as err:
        print(f"error: {err}", file=sys.stderr)
        return 2
    predicates = {
        "gt": result > 0,
        "ge": result >= 0,
        "eq": result == 0,
        "lt": result < 0,
        "le": result <= 0,
    }
    return 0 if predicates[sys.argv[1]] else 1


if __name__ == "__main__":
    raise SystemExit(main())

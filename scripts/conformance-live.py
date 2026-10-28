#!/usr/bin/env python3
"""Select a bounded command differential run."""

import argparse
import os
from pathlib import Path
import subprocess


def selection(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seed", type=int, action="append", default=[])
    parser.add_argument("--programs", type=int, default=16)
    parser.add_argument("--generated-only", action="store_true")
    parser.add_argument("--validate", action="store_true", help=argparse.SUPPRESS)
    args = parser.parse_args(argv)
    if len(args.seed) > 64 or len(set(args.seed)) != len(args.seed):
        parser.error("select at most 64 distinct seeds")
    if any(seed < 0 or seed >= 2**64 for seed in args.seed):
        parser.error("a seed must be an unsigned 64-bit integer")
    if not 1 <= args.programs <= 64:
        parser.error("program count must be between 1 and 64")
    return args


def main():
    args = selection()
    if args.validate:
        return 0
    root = Path(__file__).resolve().parent.parent
    environment = os.environ.copy()
    environment["PW_CONFORMANCE_SEEDS"] = ",".join(map(str, args.seed))
    environment["PW_CONFORMANCE_PROGRAMS"] = str(args.programs)
    pattern = "^TestLiveGeneratedDifferential$" if args.generated_only else "^TestLive(Reference|Generated)Differential$"
    return subprocess.call([
        "bash", str(root / "scripts/go-test-digest.sh"), "--full", "--tags", "referenceuserland",
        "--name", "test:conformance:live", "--", "./test/wiring/conformance/...",
        "-run", pattern, "-count=1",
    ], cwd=root, env=environment)


if __name__ == "__main__":
    raise SystemExit(main())

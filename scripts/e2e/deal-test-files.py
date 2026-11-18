"""Deal Playwright spec files to shards, longest first, and write one --test-list file per shard.

Reads `playwright test --list --reporter=json` on stdin and, optionally, measured spec durations
(`{"spec.ts": seconds}`). A spec whose tests are defined in a helper module also lists that module,
because --test-list matches tests by their location file; specs that share such a module are dealt
together.
"""

import argparse
import json
import math
from pathlib import Path
from statistics import median
import sys


def locations(suite):
    found = {spec["file"] for spec in suite.get("specs", [])}
    for child in suite.get("suites", []):
        found |= locations(child)
    return found


def groups(specs):
    """Join specs that share a test location: --test-list selects by location, so they share a shard."""
    merged = []
    for suite in specs:
        files = {suite["file"], *locations(suite)}
        joined = [group for group in merged if group["files"] & files]
        group = {"specs": [suite["file"]], "files": files}
        for other in joined:
            merged.remove(other)
            group["specs"] += other["specs"]
            group["files"] |= other["files"]
        merged.append(group)
    return merged


def durations(measured):
    """Measured seconds per spec; a spec without one is weighed at the median, or 1 when none is known."""
    known = {name: seconds for name, seconds in (measured if isinstance(measured, dict) else {}).items()
             if type(seconds) in (int, float) and math.isfinite(seconds) and seconds > 0}
    typical = median(known.values()) if known else 1
    return lambda spec: known.get(spec, typical)


def deal(listing, total, measured=None):
    if total < 1:
        raise ValueError("the shard count must be positive")
    if listing.get("errors"):
        raise ValueError("Playwright could not list the suite: "
                         + "; ".join(error.get("message", "") for error in listing["errors"]))
    specs = sorted(listing["suites"], key=lambda suite: suite["file"])
    if not specs:
        raise ValueError("the E2E selection contains no specs")
    seconds = durations(measured)
    weighed = [(sum(seconds(spec) for spec in group["specs"]), group) for group in groups(specs)]
    shards, loads = [[] for _ in range(total)], [0] * total
    # Longest first onto the least-loaded shard; equal weights fall back to dealing in name order.
    for weight, group in sorted(weighed, key=lambda row: (-row[0], min(row[1]["specs"]))):
        target = min(range(total), key=lambda index: (loads[index], index))
        loads[target] += weight
        heads = sorted(group["specs"])
        shards[target] += [*heads, *sorted(group["files"] - set(heads))]
    return shards


def main(arguments):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("total", type=int)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--timings", type=Path, help="measured seconds per spec file")
    args = parser.parse_args(arguments)
    measured = json.loads(args.timings.read_text()) if args.timings else None
    args.directory.mkdir(parents=True, exist_ok=True)
    for index, files in enumerate(deal(json.load(sys.stdin), args.total, measured), start=1):
        (args.directory / f"shard-{index}.txt").write_text("".join(f"{file}\n" for file in files))
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))

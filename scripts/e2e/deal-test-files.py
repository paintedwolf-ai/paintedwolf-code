"""Deal Playwright spec files to shards in turn and write one --test-list file per shard.

Reads `playwright test --list --reporter=json` on stdin. A spec whose tests are defined in a
helper module also lists that module, because --test-list matches tests by their location file;
specs that share such a module are dealt together.
"""

import json
from pathlib import Path
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
    return sorted(merged, key=lambda group: min(group["specs"]))


def deal(listing, total):
    if total < 1:
        raise ValueError("the shard count must be positive")
    if listing.get("errors"):
        raise ValueError("Playwright could not list the suite: "
                         + "; ".join(error.get("message", "") for error in listing["errors"]))
    specs = sorted(listing["suites"], key=lambda suite: suite["file"])
    if not specs:
        raise ValueError("the E2E selection contains no specs")
    shards = [[] for _ in range(total)]
    for index, group in enumerate(groups(specs)):
        heads = sorted(group["specs"])
        shards[index % total] += [*heads, *sorted(group["files"] - set(heads))]
    return shards


def main():
    total, directory = int(sys.argv[1]), Path(sys.argv[2])
    directory.mkdir(parents=True, exist_ok=True)
    for index, files in enumerate(deal(json.load(sys.stdin), total), start=1):
        (directory / f"shard-{index}.txt").write_text("".join(f"{file}\n" for file in files))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

"""Deal Playwright spec files to shards in turn and write one --test-list file per shard.

Reads `playwright test --list --reporter=json` on stdin. A spec whose tests are defined in a
helper module also lists that module, because --test-list matches tests by their location file.
"""

import json
from pathlib import Path
import sys


def locations(suite):
    found = {spec["file"] for spec in suite.get("specs", [])}
    for child in suite.get("suites", []):
        found |= locations(child)
    return found


def deal(listing, total):
    if listing.get("errors"):
        raise ValueError("Playwright could not list the suite: "
                         + "; ".join(error.get("message", "") for error in listing["errors"]))
    specs = sorted(listing["suites"], key=lambda suite: suite["file"])
    shards = [[] for _ in range(total)]
    for index, suite in enumerate(specs):
        shards[index % total] += [suite["file"], *sorted(locations(suite) - {suite["file"]})]
    return shards


def main():
    total, directory = int(sys.argv[1]), Path(sys.argv[2])
    directory.mkdir(parents=True, exist_ok=True)
    for index, files in enumerate(deal(json.load(sys.stdin), total), start=1):
        (directory / f"shard-{index}.txt").write_text("".join(f"{file}\n" for file in files))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

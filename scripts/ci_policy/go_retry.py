"""Retry pinned Go input downloads after transient proxy transport failures."""

import argparse
from pathlib import Path
import subprocess
import sys
import time


def run_go(command, *, cwd, env=None, attempts=3, delay_seconds=2):
    if attempts < 1:
        raise ValueError("Go input attempts must be positive")
    for attempt in range(1, attempts + 1):
        try:
            return subprocess.run(command, cwd=cwd, env=env, check=True)
        except subprocess.CalledProcessError:
            if attempt == attempts:
                raise
            print(f"Go input fetch failed; retrying ({attempt}/{attempts})", file=sys.stderr, flush=True)
            time.sleep(delay_seconds)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("module_root", type=Path)
    args = parser.parse_args()
    run_go(["go", "mod", "download"], cwd=args.module_root)


if __name__ == "__main__":
    main()

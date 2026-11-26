"""Maintain Go build cache budgets and abandoned Go temp between verification operations."""

import argparse
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time

from verification_resources import EXCLUSIVE, Reservation

GIB = 1024 ** 3
SCAN_INTERVAL = 60
# Go refreshes an entry's modification time when it uses one older than an hour, so only entries
# unused for that long are candidates, and each is rechecked just before removal.
RECENT_SECONDS = 3600
# Go refreshes an entry's timestamp only once it is over an hour old, so a running build can hold
# entries the age cutoff would evict; eviction therefore waits for every other operation to drain.
MAINTENANCE = EXCLUSIVE
ENTRY_NAME = re.compile(r"[0-9a-f]{64}-[ad]\Z")
# Work directories of the go command and its linker.
GO_TEMP_PREFIXES = ("go-build", "go-link-")
# Go removes its work directory when a command exits, and no build goes this long without adding to it.
ABANDONED_TEMP_SECONDS = 24 * 3600
TEMP_SCAN_INTERVAL = 3600


@dataclass(frozen=True)
class Budget:
    maximum: int = 20 * GIB
    target: int = 16 * GIB

    @classmethod
    def environment(cls, environment):
        maximum = int(environment.get("GOCACHE_MAX_GIB", "20")) * GIB
        target = int(environment.get("GOCACHE_TARGET_GIB", str(max(1, maximum * 4 // 5 // GIB)))) * GIB
        if not 0 < target <= maximum:
            raise ValueError("Go cache budgets require 0 < GOCACHE_TARGET_GIB <= GOCACHE_MAX_GIB")
        return cls(maximum, target)


@dataclass(frozen=True)
class Entry:
    path: Path
    modified: int
    size: int
    inode: int
    directory: bool


def allocated(info):
    return info.st_blocks * 512 if hasattr(info, "st_blocks") else info.st_size


def cache_entries(root):
    """Yield recognized entries without following links or visiting fuzz inputs."""
    if not root.is_dir():
        return
    for bucket in range(256):
        directory = root / f"{bucket:02x}"
        if directory.is_symlink() or not directory.is_dir():
            continue
        for path in directory.iterdir():
            if not ENTRY_NAME.fullmatch(path.name):
                continue
            try:
                info = path.lstat()
                is_directory = stat.S_ISDIR(info.st_mode)
                if is_directory:
                    if not path.name.endswith("-d"):
                        continue
                    # Executable access refreshes the containing directory's mtime.
                    children = list(path.iterdir())
                    child_info = [child.lstat() for child in children]
                    if any(not stat.S_ISREG(child.st_mode) for child in child_info):
                        continue
                    size = sum(allocated(child) for child in child_info)
                elif stat.S_ISREG(info.st_mode):
                    size = allocated(info)
                else:
                    continue
                yield Entry(path, info.st_mtime_ns, size, info.st_ino, is_directory)
            except FileNotFoundError:
                continue


def trim(root, budget, *, now=None, unused_seconds=None):
    now = time.time() if now is None else now
    entries = sorted(cache_entries(root), key=lambda entry: (entry.modified, str(entry.path)))
    before = remaining = sum(entry.size for entry in entries)
    removed = 0
    over_budget = before > budget.maximum
    cutoff = (now - RECENT_SECONDS) * 1e9
    for entry in entries:
        aged = unused_seconds is not None and entry.modified < (now - unused_seconds) * 1e9
        pressure = over_budget and remaining > budget.target and entry.modified < cutoff
        if not aged and not pressure:
            continue
        try:
            current = entry.path.lstat()
            # Concurrent access can refresh or replace an entry during the scan.
            if (current.st_ino, current.st_mtime_ns) != (entry.inode, entry.modified):
                continue
            if entry.directory:
                shutil.rmtree(entry.path)
            else:
                entry.path.unlink()
            remaining -= entry.size
            removed += 1
        except FileNotFoundError:
            continue
    return {"cache": str(root), "before_bytes": before, "after_bytes": remaining,
            "removed_entries": removed, "reclaimed_bytes": before - remaining,
            "over_budget": remaining > budget.maximum}


def report(result, budget):
    message = (f"go cache: {result['before_bytes'] / GIB:.2f} → {result['after_bytes'] / GIB:.2f} GiB; "
               f"reclaimed {result['reclaimed_bytes'] / GIB:.2f} GiB "
               f"({result['removed_entries']} entries); budget {budget.maximum / GIB:g} GiB; {result['cache']}")
    if result["over_budget"]:
        message += "; still over budget: recently used entries are protected; retry at the next verification boundary"
    print(message, file=sys.stderr, flush=True)


def measurement(root, size, budget):
    return {"cache": str(root), "before_bytes": size, "after_bytes": size,
            "removed_entries": 0, "reclaimed_bytes": 0, "over_budget": size > budget.maximum}


def host_temp(environment):
    return Path(environment.get("PW_HOST_TMPDIR") or environment.get("TMPDIR") or tempfile.gettempdir())


def last_touched(path):
    """Newest of a directory and its children: Go adds a child per build action, so a live build stays fresh."""
    newest = path.lstat().st_mtime
    for child in path.iterdir():
        try:
            newest = max(newest, child.lstat().st_mtime)
        except FileNotFoundError:
            continue
    return newest


def tree_size(path):
    total = 0
    for directory, _, files in os.walk(path):
        for name in files:
            try:
                total += allocated(os.lstat(os.path.join(directory, name)))
            except FileNotFoundError:
                continue
    return total


def sweep_go_temp(root, *, now=None):
    """Remove Go work directories that commands run outside verification left behind when they were killed.

    Scheduled operations keep theirs in leased run directories, so this covers only the host temp directory.
    """
    now = time.time() if now is None else now
    removed, reclaimed, failures = 0, 0, []
    entries = sorted(root.iterdir()) if root.is_dir() else []
    for path in entries:
        if not path.name.startswith(GO_TEMP_PREFIXES):
            continue
        try:
            info = path.lstat()
            if not stat.S_ISDIR(info.st_mode) or (hasattr(os, "getuid") and info.st_uid != os.getuid()):
                continue
            if last_touched(path) >= now - ABANDONED_TEMP_SECONDS:
                continue
            size = tree_size(path)
            shutil.rmtree(path)
        except FileNotFoundError:
            continue
        except OSError as error:
            failures.append(f"{path}: {error}")
            continue
        removed += 1
        reclaimed += size
    return {"temp": str(root), "removed_directories": removed, "reclaimed_bytes": reclaimed, "failures": failures}


def report_temp(result):
    if result["removed_directories"]:
        print(f"go temp: removed {result['removed_directories']} abandoned work directories "
              f"({result['reclaimed_bytes'] / GIB:.2f} GiB) from {result['temp']}", file=sys.stderr, flush=True)
    for failure in result["failures"]:
        print(f"go temp: keeping {failure}", file=sys.stderr, flush=True)


class Maintenance:
    def __init__(self, queue, environment):
        self.queue = queue
        self.environment = environment
        self.root = None
        self.retry_at = 0.0

    def sweep_temp(self):
        """Sweep abandoned Go work directories at most once per TEMP_SCAN_INTERVAL across all runners."""
        try:
            temp = host_temp(self.environment).resolve()
            key = hashlib.sha256(str(temp).encode()).hexdigest()[:24]
            path = self.queue.root / f"go-temp-{key}.json"
            with self.queue.locked():
                try:
                    checked = json.loads(path.read_text())["checked_at"]
                    if 0 <= time.time() - checked < TEMP_SCAN_INTERVAL:
                        return
                except (OSError, ValueError, KeyError, TypeError):
                    pass
                temporary = path.with_suffix(".tmp")
                temporary.write_text(json.dumps({"checked_at": time.time()}))
                temporary.replace(path)
            report_temp(sweep_go_temp(temp))
        except OSError as error:
            print(f"go temp: automatic maintenance failed: {error}", file=sys.stderr, flush=True)

    def configuration(self):
        budget = Budget.environment(self.environment)
        if self.root is None:
            value = self.environment.get("GOCACHE") or subprocess.check_output(
                ["go", "env", "GOCACHE"], env=self.environment, text=True, timeout=30).strip()
            self.root = Path(value).resolve() if value and value != "off" else False
        return budget

    def state_path(self, budget):
        key = hashlib.sha256(f"{self.root}:{budget}".encode()).hexdigest()[:24]
        return self.queue.root / f"go-cache-{key}.json"

    def due(self, budget):
        if not self.root:
            return False
        try:
            checked = json.loads(self.state_path(budget).read_text())["checked_at"]
            return not 0 <= time.time() - checked < SCAN_INTERVAL
        except (OSError, ValueError, KeyError, TypeError):
            return True

    def admitted(self):
        """Trim under the caller's maintenance or exclusive admission; report failures separately."""
        self.sweep_temp()
        try:
            budget = self.configuration()
            if not self.due(budget):
                return
            result = trim(self.root, budget)
            self.record(result, budget)
        except (OSError, ValueError, subprocess.SubprocessError) as error:
            print(f"go cache: automatic maintenance failed: {error}", file=sys.stderr, flush=True)

    def record(self, result, budget):
        report(result, budget)
        state = {**result, "checked_at": time.time(), "maximum_bytes": budget.maximum,
                 "target_bytes": budget.target}
        with self.queue.locked():
            path = self.state_path(budget)
            temporary = path.with_suffix(".tmp")
            temporary.write_text(json.dumps(state))
            temporary.replace(path)

    def scheduled(self, batch):
        """Trim if admitted at once; otherwise skip and retry after SCAN_INTERVAL. Waiting could block on
        capacity held by work that only finishing can free."""
        self.sweep_temp()
        if time.monotonic() < self.retry_at:
            return
        try:
            budget = self.configuration()
            if not self.due(budget):
                return
            # Read-only measurement can overlap active builds.
            size = sum(entry.size for entry in cache_entries(self.root))
            if size <= budget.maximum:
                self.record(measurement(self.root, size, budget), budget)
                return
            with Reservation(self.queue, self.queue.lock_file, batch, "Go cache maintenance", MAINTENANCE,
                             1) as reservation:
                if reservation.try_acquire():
                    self.admitted()
                else:
                    self.retry_at = time.monotonic() + SCAN_INTERVAL
        except (OSError, ValueError, subprocess.SubprocessError) as error:
            print(f"go cache: automatic maintenance failed: {error}", file=sys.stderr, flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("cache", type=Path, nargs="?")
    parser.add_argument("--unused-hours", type=int)
    parser.add_argument("--abandoned-temp", type=Path, help="host temp directory to sweep for abandoned Go work")
    args = parser.parse_args()
    if args.cache is None and args.abandoned_temp is None:
        parser.error("expected a cache, --abandoned-temp, or both")
    if args.unused_hours is not None and args.unused_hours < 0:
        parser.error("--unused-hours must be nonnegative")
    if args.abandoned_temp is not None:
        report_temp(sweep_go_temp(args.abandoned_temp))
    if args.cache is not None:
        budget = Budget.environment(os.environ)
        result = trim(args.cache, budget,
                      unused_seconds=None if args.unused_hours is None else args.unused_hours * 3600)
        report(result, budget)


if __name__ == "__main__":
    main()

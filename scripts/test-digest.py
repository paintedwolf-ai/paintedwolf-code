#!/usr/bin/env python3
"""Condense structured test and compiler output into a failure digest."""

from __future__ import annotations

import argparse
import json
import os
import sys
from collections import defaultdict

from verification_timings import seconds, write_slowest


def _display_tests(
    out,
    pkg: str,
    tests: list[str],
    output: dict[tuple[str, str], list[str]],
    max_lines: int,
) -> None:
    """Print failing tests with subtest hierarchy (TestRoot/sub/name)."""
    by_root: dict[str, list[tuple[str, str]]] = defaultdict(list)
    standalone: list[str] = []

    for test in sorted(set(tests)):
        if "/" in test:
            root, rest = test.split("/", 1)
            by_root[root].append((test, rest))
        else:
            standalone.append(test)

    for test in standalone:
        out.write(f"      {test}\n")
        for ln in output.get((pkg, test), [])[:max_lines]:
            out.write(f"        {ln.strip()}\n")

    for root in sorted(by_root):
        subs = sorted(by_root[root], key=lambda item: item[1])
        if len(subs) == 1:
            full, sub = subs[0]
            out.write(f"      {root}\n")
            out.write(f"        {sub}\n")
            for ln in output.get((pkg, full), [])[:max_lines]:
                out.write(f"          {ln.strip()}\n")
            continue
        out.write(f"      {root}\n")
        for full, sub in subs:
            out.write(f"        {sub}\n")
            for ln in output.get((pkg, full), [])[:max_lines]:
                out.write(f"          {ln.strip()}\n")


def _write_failed_pkgs(last_run_dir: str | None, pkgs: set[str]) -> None:
    if not last_run_dir or not pkgs:
        return
    os.makedirs(last_run_dir, exist_ok=True)
    path = os.path.join(last_run_dir, "failed-go-pkgs.txt")
    with open(path, "w", encoding="utf-8") as fh:
        for pkg in sorted(pkgs):
            fh.write(f"{pkg}\n")


def _test_timings(timings, cached):
    families = {}
    for (pkg, test), elapsed in timings.items():
        if not test or pkg in cached:
            continue
        key = (pkg, test.split("/", 1)[0])
        previous = families.get(key)
        if previous is None or elapsed > previous[1] or (elapsed == previous[1] and len(test) < len(previous[0])):
            families[key] = (test, elapsed)
    return ((f"{pkg} {test}", elapsed) for (pkg, _), (test, elapsed) in families.items())


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--name", default="go test", help="label for this run in the digest header")
    parser.add_argument("--process-rc", type=int, default=0, help="Go process exit code")
    parser.add_argument(
        "--raw-log",
        help="repo-relative path to the captured JSON log (shown on failure)",
    )
    parser.add_argument(
        "--lines",
        type=int,
        default=12,
        help="max assertion lines per failing test (default: 12)",
    )
    args = parser.parse_args(argv)

    raw_path = args.raw_log
    last_run_dir = os.environ.get("LAST_RUN_DIR")

    output: dict[tuple[str, str], list[str]] = defaultdict(list)
    failed_tests: list[tuple[str, str]] = []
    failed_pkgs: set[str] = set()
    build_failed_pkgs: set[str] = set()
    build_errors: list[str] = []
    packages_seen: set[str] = set()
    packages_finished: set[str] = set()
    timings: dict[tuple[str, str], float] = {}
    cached: set[str] = set()
    test_results: dict[tuple[str, str], str] = {}
    packages_with_tests: set[str] = set()

    for line in sys.stdin:
        line = line.strip()
        # Module fetches on a cold cache report progress on the merged stream.
        if not line or line.startswith("go: downloading "):
            continue
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            build_errors.append(line)
            continue
        if not isinstance(ev, dict) or any(
            not isinstance(ev.get(field, ""), str)
            for field in ("Package", "Test", "Action", "ImportPath", "Output")
        ):
            build_errors.append("Invalid test event: " + line)
            continue

        pkg = ev.get("Package", "")
        test = ev.get("Test", "")
        action = ev.get("Action", "")
        if pkg:
            packages_seen.add(pkg)
            if not test and action in {"pass", "fail", "skip"}:
                packages_finished.add(pkg)
        key = (pkg, test)
        if pkg and test:
            packages_with_tests.add(pkg)
            if action in {"pass", "fail", "skip"}:
                test_results[key] = action
        elapsed = seconds(ev.get("Elapsed"))
        if action in {"pass", "fail"} and pkg and elapsed is not None:
            timings[key] = max(timings.get(key, 0), elapsed)
        if action == "output" and not test and "(cached)" in ev.get("Output", ""):
            cached.add(pkg)

        if action == "build-output":
            output[(ev.get("ImportPath", ""), "")].append(ev.get("Output", "").rstrip("\n"))
        elif action == "build-fail":
            build_failed_pkgs.add(ev.get("ImportPath", ""))
        elif action == "output":
            text = ev.get("Output", "")
            stripped = text.strip()
            if stripped in ("PASS", "FAIL") or stripped.startswith(
                ("ok  ", "=== RUN", "=== PAUSE", "=== CONT", "--- PASS")
            ):
                continue
            output[key].append(text.rstrip("\n"))
        elif action == "pass":
            output.pop(key, None)
        elif action == "fail":
            if test:
                failed_tests.append((pkg, test))
            else:
                failed_pkgs.add(pkg)

    if not packages_seen:
        build_errors.append("No package results were captured; verification is incomplete")
    for pkg in sorted(packages_seen - packages_finished):
        build_errors.append(f"{pkg}: no terminal package result; verification is incomplete")
    if args.process_rc:
        build_errors.append(f"Go exited with status {args.process_rc}")

    out = sys.stdout
    by_pkg: dict[str, list[str]] = defaultdict(list)
    for pkg, test in failed_tests:
        by_pkg[pkg].append(test)

    all_failed_pkgs = set(by_pkg) | failed_pkgs | build_failed_pkgs
    has_failures = bool(all_failed_pkgs or build_errors)

    def write_coverage():
        counts = {action: sum(result == action for result in test_results.values())
                  for action in ("pass", "fail", "skip")}
        out.write(f"    test results: {counts['pass']} passed, {counts['fail']} failed, "
                  f"{counts['skip']} skipped\n")
        empty = len(packages_finished - packages_with_tests)
        out.write(f"    {empty} package{'s' if empty != 1 else ''} without tests\n")

    def write_timings():
        write_slowest(out, "packages", ((pkg, elapsed) for (pkg, test), elapsed in timings.items()
                                       if not test and pkg not in cached))
        write_slowest(out, "tests", _test_timings(timings, cached))

    if not has_failures:
        n = len(packages_seen)
        label = f"{n} package{'s' if n != 1 else ''}" if n else "no packages"
        out.write(f"\n  ✓ {args.name} — {label}, no failures\n")
        write_coverage()
        write_timings()
        if last_run_dir:
            failed_path = os.path.join(last_run_dir, "failed-go-pkgs.txt")
            if os.path.isfile(failed_path):
                os.remove(failed_path)
        return 0

    out.write(f"\n  ✗ {args.name}\n")
    write_coverage()

    if build_errors:
        out.write("    build/setup errors:\n")
        for line in build_errors[:20]:
            out.write(f"      {line}\n")

    for pkg in sorted(by_pkg):
        out.write(f"    {pkg}\n")
        _display_tests(out, pkg, by_pkg[pkg], output, args.lines)

    bare = sorted((failed_pkgs | build_failed_pkgs) - set(by_pkg))
    for pkg in bare:
        status = "build failed" if pkg in build_failed_pkgs else "FAIL (no test-level detail; see log)"
        out.write(f"    {pkg}: {status}\n")
        for ln in output.get((pkg, ""), [])[: args.lines]:
            out.write(f"        {ln.strip()}\n")

    n = len(failed_tests)
    out.write(
        f"    → {n} failing test{'s' if n != 1 else ''} "
        f"across {len(all_failed_pkgs)} package{'s' if len(all_failed_pkgs) != 1 else ''}\n"
    )
    if raw_path:
        out.write(f"    full log: {raw_path}\n")
    out.write("    re-run failures: ./task test:failed\n")

    _write_failed_pkgs(last_run_dir, all_failed_pkgs)
    write_timings()
    return 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))

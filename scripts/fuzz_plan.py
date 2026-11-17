"""Run the Go fuzz targets the verification plan declares.

Every target is declared in the plan's fuzz section with the seconds of
exploration it is given. That time is what the fuzzer gets, never a limit
that fails a run: a target fails only on a crasher. A target the source
declares and the plan does not, or the reverse, fails the run before any
fuzzing, so the plan stays the one list of targets.

`run` explores the declared targets of the requested packages for their
declared time. `smoke` explores, for the plan's smoke seconds, every target in
a package the change touches directly; seeds and committed corpus run first.
"""

import argparse
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys

import change_report
from verification_plan import FUZZ_TARGET, GO_MODULE, catalog

MODULE = GO_MODULE.name
SOURCE_TARGET = re.compile(r"^func (Fuzz\w*)\(", re.M)


def declaration(plan=None):
    """The plan's fuzz section: listing tags, smoke seconds and seconds per target."""
    return (plan or catalog())["fuzz"]


def go(*args, capture=True):
    return subprocess.run(["go", *args], cwd=GO_MODULE, capture_output=capture, text=True)


def test_files(tags, patterns):
    """{import path: (./relative directory, test files the tags select)} for packages under patterns."""
    result = go("list", "-e", f"-tags={','.join(tags)}", "-f",
                "{{.ImportPath}}\t{{.Dir}}\t{{join .TestGoFiles \" \"}} {{join .XTestGoFiles \" \"}}", *patterns)
    if result.returncode:
        raise RuntimeError(f"go list: {result.stderr.strip()}")
    packages, module = {}, GO_MODULE.resolve()
    for line in result.stdout.splitlines():
        path, directory, files = line.split("\t")
        packages[path] = ("./" + Path(directory).relative_to(module).as_posix(),
                          [Path(directory, name) for name in files.split()])
    return packages


def listed(tags, patterns):
    """{package: [targets]} for packages under patterns, by go test -list over those with a Fuzz function."""
    candidates = {path: package for path, (package, files) in test_files(tags, patterns).items()
                  if any(SOURCE_TARGET.search(file.read_text(errors="replace")) for file in files)}
    if not candidates:
        return {}
    result = go("test", f"-tags={','.join(tags)}", "-list=^Fuzz", *sorted(candidates.values()))
    if result.returncode:
        raise RuntimeError(f"go test -list: {result.stdout}{result.stderr}".strip())
    targets, pending = {}, []
    for line in result.stdout.splitlines():
        fields = line.split()
        if FUZZ_TARGET.fullmatch(line):
            pending.append(line)
        elif len(fields) > 1 and fields[0] == "ok" and fields[1] in candidates:
            if pending:
                targets[candidates[fields[1]]] = pending
            pending = []
    return targets


def mismatches(found, declared, complete):
    """Targets the source and the plan disagree on. An incomplete listing cannot see stale declarations."""
    problems = [f"{package} {target} is not declared in the plan's fuzz section"
                for package, targets in sorted(found.items()) for target in targets
                if target not in declared.get(package, {})]
    if complete:
        problems += [f"{package} {target} is declared but no test file defines it"
                     for package, targets in sorted(declared.items()) for target in sorted(targets)
                     if target not in found.get(package, [])]
    return problems


def touched_packages(paths):
    """./relative package directories holding a changed file, or a changed file under their testdata."""
    packages = set()
    for path in paths:
        parts = PurePosixPath(path).parts
        if len(parts) < 2 or parts[0] != MODULE:
            continue
        directory = parts[1:-1]
        if "testdata" in directory:
            directory = directory[:directory.index("testdata")]
        if directory and (GO_MODULE.joinpath(*directory)).is_dir():
            packages.add("./" + "/".join(directory))
    return sorted(packages)


def schedule(mode, packages, plan=None):
    """(package, target, seconds) to explore, after checking the plan against the source."""
    fuzz = declaration(plan)
    if mode == "smoke":
        scope = change_report.resolve(GO_MODULE.parent)
        print(f"fuzz smoke: {scope.describe() if scope else 'no change base'}", file=sys.stderr)
        packages = touched_packages(scope.paths) if scope else []
        if not packages:
            return []
    found = listed(fuzz["tags"], packages or ["./..."])
    problems = mismatches(found, fuzz["targets"], complete=mode == "run" and not packages)
    if problems:
        raise ValueError("\n".join(problems))
    return [(package, target, fuzz["smoke_seconds"] if mode == "smoke" else fuzz["targets"][package][target])
            for package, targets in sorted(found.items()) for target in targets]


def explore(package, target, seconds, tags, workers):
    command = ["test", f"-tags={','.join(tags)}", "-timeout=0", f"-parallel={workers}", "-run=^$",
               f"-fuzz=^{target}$", f"-fuzztime={seconds}s", package]
    return go(*command, capture=False).returncode


def summary(results):
    rows = ["| Package | Target | Seconds | Result |", "|---|---|---|---|"]
    rows += [f"| `{package}` | `{target}` | {seconds} | {'passed' if code == 0 else 'failed'} |"
             for package, target, seconds, code in results]
    return "\n".join(rows) + "\n"


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("mode", choices=["run", "smoke"])
    parser.add_argument("packages", nargs="*", help="./relative package patterns; run only")
    args = parser.parse_args(argv)
    if args.mode == "smoke" and args.packages:
        parser.error("smoke selects the packages the change touches")
    packages = [] if args.packages == ["./..."] else args.packages
    try:
        planned = schedule(args.mode, packages)
    except (RuntimeError, ValueError) as error:
        print(f"fuzz: {error}", file=sys.stderr)
        return 1
    if not planned:
        print("fuzz: no declared target in scope", file=sys.stderr)
        return 0
    tags = declaration()["tags"]
    workers = os.environ["PW_FUZZ_WORKERS"]
    results = []
    for package, target, seconds in planned:
        print(f"fuzz: {package} {target}: exploring {seconds}s with {workers} worker(s)", file=sys.stderr, flush=True)
        results.append((package, target, seconds, explore(package, target, seconds, tags, workers)))
    report = summary(results)
    print(f"fuzz: {sum(seconds for _, _, seconds, _ in results)}s declared over {len(results)} target(s)\n{report}",
          file=sys.stderr)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a") as output:
            output.write(f"### Fuzz {args.mode}\n\n{report}\n")
    return 1 if any(code for *_, code in results) else 0


if __name__ == "__main__":
    sys.exit(main())

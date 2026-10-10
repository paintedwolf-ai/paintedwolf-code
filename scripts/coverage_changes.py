"""Coverage of the statements a change adds or modifies.

`go` measures each changed package with its own short tests, then measures a
package that falls short again with the short tests that transitively reach it,
crediting coverage across them. `den` runs the Vitest tests related to the
changed files and measures those files. A unit (a Go package, a Den file)
fails when more than `grace` of its changed statements are uncovered and fewer
than `percent` of them are covered (scripts/coverage-policy.json). Code a
change does not touch never fails; low package coverage is reported as a
notice for the packages a change touches.
"""

import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys

from artifact_paths import artifact_root
import change_report
from change_report import Finding
import coverage_policy
import verification_plan

ROOT = Path(__file__).resolve().parent.parent
GO_DIR = ROOT / "lycaon"
GENERATED_GO = re.compile(r"^// Code generated .* DO NOT EDIT\.$", re.M)
FUNCTION = re.compile(r"^func .*\{", re.M)
DEN_SOURCE = re.compile(r"^lycaon-den/src/.+\.tsx?$")
DEN_EXCLUDED = re.compile(r"(\.test\.|\.spec\.|\.d\.ts$|\.generated\.|/test/|/mocks/|/__tests__/)")


class Unit:
    """Changed statements of one gated unit and where the uncovered ones are."""

    def __init__(self, name):
        self.name = name
        self.statements = 0
        self.uncovered = 0
        self.gaps = {}  # path → uncovered changed line numbers

    def add(self, path, first, last, statements, covered, changed):
        self.statements += statements
        if not covered:
            self.uncovered += statements
            self.gaps.setdefault(path, set()).update(line for line in range(first, last + 1) if line in changed)

    def percent(self):
        return 100 * (self.statements - self.uncovered) / self.statements if self.statements else 100.0


def judge(area, units, floor):
    """Findings for gated units: errors past the floor, notices for other gaps."""
    findings, failed = [], False
    for unit in sorted(units.values(), key=lambda u: u.name):
        if not unit.statements:
            continue
        summary = (f"{unit.name}: {unit.statements - unit.uncovered} of {unit.statements} changed statements "
                   f"covered ({unit.percent():.0f}%)")
        fails = unit.uncovered > floor["grace"] and unit.percent() < floor["percent"]
        if fails:
            failed = True
            findings.append(Finding("error", area, f"{summary}, below {floor['percent']}% with more than "
                                    f"{floor['grace']} uncovered; test the changed behavior"))
        elif unit.uncovered:
            findings.append(Finding("info", area, summary))
        level = "error" if fails else "notice"
        for path, lines in sorted(unit.gaps.items()):
            for first, last in change_report.ranges(lines):
                findings.append(Finding(level, area, "changed lines not covered by tests", path, first, last))
    return findings, failed


# Go

def module_path():
    return (GO_DIR / "go.mod").read_text().splitlines()[0].split()[1]


def exempt_packages():
    out = []
    for line in (GO_DIR / "coverage-exempt.txt").read_text().splitlines():
        line = line.split("#", 1)[0].strip()
        if line:
            out.append(line)
    return out


def go_packages():
    fields = ("{{.ImportPath}}\t{{.Dir}}\t{{join .Imports \" \"}}\t{{join .TestImports \" \"}} {{join .XTestImports \" \"}}"
              "\t{{len .TestGoFiles}} {{len .XTestGoFiles}}")
    listing = subprocess.run(["go", "list", "-e", "-f", fields, "./..."], cwd=GO_DIR, capture_output=True, text=True, check=True)
    packages = {}
    for row in listing.stdout.splitlines():
        path, directory, imports, test_imports, tests = row.split("\t")
        packages[path] = {"dir": Path(directory).resolve().relative_to(ROOT).as_posix(),
                          "imports": set(imports.split()), "test_imports": set(test_imports.split()),
                          "tested": sum(map(int, tests.split())) > 0}
    return packages


def go_test_importers(packages, targets, tiers):
    """Follow production dependencies; test-only edges terminate at their test binary."""
    reached = set(targets)
    while True:
        expanded = reached | {path for path, info in packages.items() if info["imports"] & reached}
        if expanded == reached:
            break
        reached = expanded
    return {path for path, info in packages.items() if info["tested"] and not tiers.search(path)
            and (path in reached or info["test_imports"] & reached)}


def go_changed_files(scope):
    out = []
    for path in scope.paths:
        file = ROOT / path
        if not path.startswith("lycaon/") or not path.endswith(".go") or path.endswith("_test.go") or not file.is_file():
            continue
        if GENERATED_GO.search(file.read_text(errors="replace")[:2048]):
            continue
        out.append(path)
    return out


def read_go_profile(profile, module):
    """Blocks by file: (first line, last line, statements, covered), merged across test binaries."""
    blocks = {}
    for row in profile.read_text().splitlines()[1:]:
        location, statements, count = row.rsplit(" ", 2)
        name, span = location.rsplit(":", 1)
        start, end = span.split(",")
        key = (name, start, end)
        covered = int(count) > 0
        previous = blocks.get(key)
        blocks[key] = (int(statements), covered or (previous is not None and previous[1]))
    by_file = {}
    for (name, start, end), (statements, covered) in blocks.items():
        path = "lycaon" + name[len(module):] if name.startswith(module) else name
        by_file.setdefault(path, []).append((int(start.split(".")[0]), int(end.split(".")[0]), statements, covered))
    return by_file


def merge_go_blocks(first, second):
    """A successful later test binary cannot erase an earlier covered block."""
    merged = {}
    for profile in (first, second):
        for path, rows in profile.items():
            blocks = merged.setdefault(path, {})
            for start, end, statements, covered in rows:
                key = (start, end, statements)
                blocks[key] = covered or blocks.get(key, False)
    return {path: [(*key, covered) for key, covered in blocks.items()] for path, blocks in merged.items()}


def measure_go(name, coverpkg, tested, module):
    """Run short tests with coverage across packages; None when tests fail."""
    profile = artifact_root(ROOT) / "coverage" / f"{name}.out"
    profile.parent.mkdir(parents=True, exist_ok=True)
    profile.unlink(missing_ok=True)
    code = subprocess.call(["bash", str(ROOT / "scripts" / "go-test-digest.sh"), "--name", "coverage:changes", "--",
                            f"-coverpkg={','.join(sorted(coverpkg))}", f"-coverprofile={profile}", *sorted(tested)],
                           cwd=GO_DIR)
    return read_go_profile(profile, module) if code == 0 and profile.exists() else None


def go_units(touched, blocks, lines, module):
    units = {}
    for package, files in touched.items():
        unit = units.setdefault(package, Unit(package.removeprefix(module + "/")))
        for path in files:
            for first, last, statements, covered in blocks.get(path, []):
                if any(line in lines.get(path, ()) for line in range(first, last + 1)):
                    unit.add(path, first, last, statements, covered, lines[path])
    return units


def run_go(scope):
    floor = coverage_policy.changed("go")
    package_floor = coverage_policy.percentage(coverage_policy.get("go.package"))
    tiers = re.compile(verification_plan.catalog()["go"]["test:short"]["exclude"])
    changed = go_changed_files(scope)
    if not changed:
        return [], False, "no changed Go source"
    module = module_path()
    packages = go_packages()
    by_dir = {info["dir"]: path for path, info in packages.items()}
    exempt = exempt_packages()
    touched = {}
    for path in changed:
        package = by_dir.get(Path(path).parent.as_posix())
        if package is None or tiers.search(package) or any(package.endswith("/" + e) for e in exempt):
            continue
        touched.setdefault(package, []).append(path)
    if not touched:
        return [], False, "changed Go source is exempt or outside measured packages"

    def importers(targets):
        return go_test_importers(packages, targets, tiers)

    findings = []
    for package in sorted(touched):
        if packages[package]["tested"] or importers({package}):
            continue
        # Declarations alone carry no statements; changed functions need a test that reaches them.
        files = [path for path in touched.pop(package) if FUNCTION.search((ROOT / path).read_text(errors="replace"))]
        if files:
            findings.append(Finding("error", "go", f"{package.removeprefix(module + '/')}: changed functions, and no "
                                    "test in the package or a package importing it", files[0]))
    measured = f"{len(touched)} changed package(s)"
    if not touched:
        return findings, bool(findings), measured
    lines = change_report.changed_lines(ROOT, scope.base, changed)
    # Packages are measured by their own tests first; one that falls short is
    # measured again with the short test binaries that transitively reach it.
    own = {package for package in touched if packages[package]["tested"]}
    blocks = measure_go("changes-go", touched, own, module) if own else {}
    if blocks is None:
        return [Finding("error", "go", "tests failed, so changed coverage was not measured; fix the tests above")], True, measured
    units = go_units(touched, blocks, lines, module)
    short = {p for p, unit in units.items() if unit.uncovered > floor["grace"] and unit.percent() < floor["percent"]}
    short |= set(touched) - own
    if short and importers(short):
        wider = measure_go("changes-go-importers", short, (own & short) | importers(short), module)
        if wider is None:
            return [Finding("error", "go", "tests failed, so changed coverage was not measured; fix the tests above")], True, measured
        blocks = merge_go_blocks(blocks, wider)
        units = go_units(touched, blocks, lines, module)
    for package in sorted(touched):
        rows = [row for path, file_rows in blocks.items() if Path(path).parent.as_posix() == packages[package]["dir"]
                for row in file_rows]
        total = sum(row[2] for row in rows)
        covered = sum(row[2] for row in rows if row[3])
        if total and coverage_policy.percentage(f"{100 * covered / total:.2f}") < package_floor:
            findings.append(Finding("notice", "go", f"{units[package].name}: package at {100 * covered / total:.1f}% "
                                    f"(floor {package_floor}%); this change touched it"))
    judged, failed = judge("go", units, floor)
    return judged + findings, failed or any(f.level == "error" for f in findings), measured


# Den

def run_den(scope):
    floor = coverage_policy.changed("den")
    changed = [p for p in scope.paths if DEN_SOURCE.match(p) and not DEN_EXCLUDED.search(p) and (ROOT / p).is_file()]
    if not changed:
        return [], False, "no changed frontend source"
    reports = artifact_root(ROOT) / "coverage" / "changes-den"
    shutil.rmtree(reports, ignore_errors=True)
    include = [f"--coverage.include={Path(p).relative_to('lycaon-den').as_posix()}" for p in changed]
    # Only tests related to the changed sources measure them, so a changed
    # config or lockfile must not widen the run to the whole suite; Vitest's
    # CLI has no flag for that, so vitest.config.ts reads PW_VITEST_CHANGED_ONLY.
    code = subprocess.call(["bash", str(ROOT / "scripts" / "vitest-digest.sh"), "--name", "den:coverage:changes", "--",
                            "--changed", scope.base, "--passWithNoTests",
                            "--coverage", "--coverage.reporter=json",
                            f"--coverage.reportsDirectory={reports}", *include], cwd=ROOT,
                           env={**os.environ, "PW_VITEST_CHANGED_ONLY": "1"})
    result = reports / "coverage-final.json"
    if code != 0 or not result.exists():
        return [Finding("error", "den", "tests failed, so changed coverage was not measured; fix the tests above")], True, \
            f"{len(changed)} changed file(s)"
    coverage = {Path(name).resolve(): data for name, data in json.loads(result.read_text()).items()}
    lines = change_report.changed_lines(ROOT, scope.base, changed)
    units = {}
    findings = []
    for path in changed:
        unit = units.setdefault(path, Unit(path.removeprefix("lycaon-den/")))
        data = coverage.get((ROOT / path).resolve())
        if data is None:
            findings.append(Finding("notice", "den", f"{unit.name}: no statements measured", path))
            continue
        for key, span in data["statementMap"].items():
            first, last = span["start"]["line"], span["end"]["line"]
            if any(line in lines.get(path, ()) for line in range(first, last + 1)):
                unit.add(path, first, last, 1, data["s"][key] > 0, lines[path])
    judged, failed = judge("den", units, floor)
    return judged + findings, failed, f"{len(changed)} changed file(s)"


def main(arguments):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("language", choices=("go", "den"))
    language = parser.parse_args(arguments).language
    title = "Changed coverage (Go)" if language == "go" else "Changed coverage (Den)"
    scope = change_report.resolve(ROOT)
    if scope is None:
        change_report.emit(ROOT, title, "no main branch is reachable, so there is no change to measure", [], False)
        return 2 if os.environ.get("GITHUB_ACTIONS") == "true" else 0
    findings, failed, measured = (run_go if language == "go" else run_den)(scope)
    change_report.emit(ROOT, title, f"{scope.describe()}; {measured}", findings, failed)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))

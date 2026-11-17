"""Run the integration-tagged Go tests of the packages a change edits directly.

A package is edited directly when it holds a changed file, or a changed file
under its testdata. The `test:integration` recipe runs those it covers; the
merge queue runs the packages that import them.
"""

import os
from pathlib import Path, PurePosixPath
import sys

import change_report
from ci_policy.impact import module_tree
from verification_plan import GO_MODULE, catalog, within

ROOT = GO_MODULE.parent
TARGET = "test:integration"


def edited_packages(paths, declared):
    """./relative package directories the recipe covers that hold a changed Go package file."""
    packages = set()
    for path in paths:
        if not module_tree(path):
            continue
        directory = PurePosixPath(path).parts[1:-1]
        if "testdata" in directory:
            directory = directory[:directory.index("testdata")]
        package = "./" + "/".join(directory)
        if any((GO_MODULE.joinpath(*directory)).glob("*.go")) and within(package, declared):
            packages.add(package)
    return sorted(packages)


def main():
    scope = change_report.resolve(ROOT)
    if scope is None:
        print(f"{TARGET}:changes: no main branch is reachable, so there is no change to test", file=sys.stderr)
        return 2 if os.environ.get("GITHUB_ACTIONS") == "true" else 0
    packages = edited_packages(scope.paths, catalog()["go"][TARGET]["packages"])
    print(f"{TARGET}:changes: {scope.describe()}; {len(packages)} edited package(s)", file=sys.stderr)
    if not packages:
        return 0
    script = str(Path(__file__).resolve().parent / "test-execution.py")
    os.execv(sys.executable, [sys.executable, script, "plan", "--", TARGET, "--", *packages])


if __name__ == "__main__":
    sys.exit(main())

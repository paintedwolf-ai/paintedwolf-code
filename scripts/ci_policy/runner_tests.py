"""Run verification unittest discovery with its pinned YAML dependency, or every declared script suite."""

from pathlib import Path
import sys
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
from toolchain import python_tools
import coverage_policy
import python_coverage

# Runs every script suite coverage policy declares, each with its own requirements, instead of this one.
EVERY_SUITE = "--every-suite"


def discovery_arguments(arguments):
    suite = coverage_policy.python_suites()["test:runner"]
    return ["unittest", "discover", "-s", str(SCRIPTS.parent / suite["start"]),
            "-p", suite["pattern"], *arguments]


def main(argv=None):
    if sys.version_info < (3, 11):
        raise SystemExit("Script verification requires Python 3.11 or newer")
    args = sys.argv[1:] if argv is None else argv
    if args == [EVERY_SUITE]:
        failed = python_coverage.run(SCRIPTS.parent, coverage_policy.python_suites())
        if failed:
            raise SystemExit(f"Script suites failed: {', '.join(failed)}")
        return
    try:
        packages = python_tools.prepared(SCRIPTS / "workflow_lint" / "requirements.txt")
    except ValueError as error:
        raise SystemExit(f"{error}; run ./task setup-dev -- --ci-inputs analysis") from error
    sys.path[:0] = [str(packages), str(SCRIPTS.parent), str(SCRIPTS)]
    unittest.main(module=None, argv=discovery_arguments(args))


if __name__ == "__main__":
    main()

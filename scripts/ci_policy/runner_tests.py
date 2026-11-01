"""Run verification unittest discovery with its pinned YAML dependency."""

from pathlib import Path
import sys
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
from toolchain import python_tools


def main(argv=None):
    if sys.version_info < (3, 10):
        raise SystemExit("Script verification requires Python 3.10 or newer")
    try:
        packages = python_tools.prepared(SCRIPTS / "workflow_lint" / "requirements.txt")
    except ValueError as error:
        raise SystemExit(f"{error}; run ./task setup-dev -- --ci-inputs analysis") from error
    sys.path[:0] = [str(packages), str(SCRIPTS.parent), str(SCRIPTS)]
    args = sys.argv[1:] if argv is None else argv
    unittest.main(module=None, argv=["unittest", "discover", "-s", str(SCRIPTS), "-p", "test_*execution.py", *args])


if __name__ == "__main__":
    main()

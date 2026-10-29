"""Coverage floors from scripts/coverage-policy.json, compared without rounding.

`coverage_policy.py get go.total` prints a floor; `coverage_policy.py 62.4`
validates a percentage; `coverage_policy.py 62.4 62` exits 1 below the floor.
"""

from decimal import Decimal, InvalidOperation
import json
from pathlib import Path
import sys

POLICY = Path(__file__).resolve().parent / "coverage-policy.json"


def percentage(value):
    try:
        number = Decimal(str(value))
    except InvalidOperation as error:
        raise ValueError("coverage percentages must be numbers between 0 and 100") from error
    if not number.is_finite() or not 0 <= number <= 100:
        raise ValueError("coverage percentages must be numbers between 0 and 100")
    return number


def policy():
    return json.loads(POLICY.read_text())


def get(key):
    """One floor by dotted key, such as go.changed.percent."""
    value = policy()
    for part in key.split("."):
        if not isinstance(value, dict) or part not in value:
            raise ValueError(f"no coverage policy value {key}")
        value = value[part]
    if isinstance(value, dict) or isinstance(value, bool):
        raise ValueError(f"coverage policy value {key} is not a number")
    return value


def changed(language):
    """The changed-statement floor: a percentage and a grace count of statements."""
    floor = {"percent": percentage(get(f"{language}.changed.percent")), "grace": get(f"{language}.changed.grace")}
    if type(floor["grace"]) is not int or floor["grace"] < 0:
        raise ValueError(f"{language}.changed.grace must be a nonnegative integer")
    return floor


def python_suites():
    """The Python suites that measure scripts: a discovery start directory, a file pattern, and optional requirements."""
    value = policy().get("python", {}).get("suites")
    if not isinstance(value, dict) or not value:
        raise ValueError("python.suites must declare at least one suite")
    for name, suite in value.items():
        if (not isinstance(suite, dict) or not {"start", "pattern"} <= set(suite) <= {"start", "pattern", "requirements"}
                or not all(isinstance(item, str) and item for item in suite.values())):
            raise ValueError(f"python suite {name} needs a start directory, a pattern, and optionally a requirements file")
    return value


def main(arguments):
    try:
        if len(arguments) == 2 and arguments[0] == "get":
            print(get(arguments[1]))
            return 0
        if len(arguments) not in (1, 2):
            raise ValueError("expected `get <key>`, or a percentage and an optional minimum")
        actual = percentage(arguments[0])
        if len(arguments) == 1:
            print(actual)
            return 0
        return 0 if actual >= percentage(arguments[1]) else 1
    except ValueError as error:
        print(f"coverage: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))

"""Validate percentage floors without rounding away threshold failures."""

from decimal import Decimal, InvalidOperation
import sys


def percentage(value):
    try:
        number = Decimal(value)
    except InvalidOperation as error:
        raise ValueError("coverage percentages must be numbers between 0 and 100") from error
    if not number.is_finite() or not 0 <= number <= 100:
        raise ValueError("coverage percentages must be numbers between 0 and 100")
    return number


def main(arguments):
    try:
        if len(arguments) not in (1, 2):
            raise ValueError("expected a percentage and an optional minimum")
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

"""Authenticate stdin against the configured credential without exposing either."""

import hmac
import os
import sys


def main():
    expected = os.environ.get("EXPECTED_TOKEN")
    if not expected:
        print("Authentication is not configured", file=sys.stderr)
        return 2
    supplied = sys.stdin.read()
    if not hmac.compare_digest(supplied.encode(), expected.encode()):
        print("Authentication rejected", file=sys.stderr)
        return 1
    print("Authenticated")
    return 0


if __name__ == "__main__":
    sys.exit(main())

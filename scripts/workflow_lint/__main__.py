"""Run GitHub Actions validation or prepare its pinned tools."""
import argparse

from . import prepare, validate


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--provision", action="store_true")
    args = parser.parse_args()
    if args.provision:
        prepare()
        return 0
    return validate()


if __name__ == "__main__":
    raise SystemExit(main())

import sys
from source import load_profiles
from ranking import rank_profiles
from assembly import format_report


def build_report(path):
    return format_report(rank_profiles(load_profiles(path)))


def main():
    try:
        print(build_report(sys.argv[1]))
    except (ValueError, OSError, IndexError) as exc:
        print(f"Cannot build report: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

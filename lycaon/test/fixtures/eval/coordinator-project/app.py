import json
import sys
from planner import build_plan


def main():
    with open(sys.argv[1], encoding="utf-8") as stream:
        jobs = json.load(stream)
    print(json.dumps(build_plan(jobs)))


if __name__ == "__main__":
    main()

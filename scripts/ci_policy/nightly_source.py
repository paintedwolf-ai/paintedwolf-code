"""The exact commit and tree one nightly run verifies."""
import os
import subprocess
import sys

from ci_policy.qualification_source import ROOT, commit, require_compatible_tooling


def select(environment):
    controller = commit(environment["GITHUB_SHA"])
    source = commit(environment.get("NIGHTLY_SOURCE") or controller)
    # The workflow and its reusable interfaces come from the dispatched commit.
    require_compatible_tooling(source, controller)
    tree = subprocess.check_output(["git", "rev-parse", source + "^{tree}"], cwd=ROOT, text=True).strip()
    return {"sha": source, "tree": tree}


def main():
    try:
        record = select(os.environ)
    except (ValueError, subprocess.CalledProcessError) as error:
        print(f"::error title=Nightly source::{error}")
        return 1
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"sha={record['sha']}\ntree={record['tree']}\n")
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
        summary.write(f"Nightly source `{record['sha']}`, tree `{record['tree']}`.\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())

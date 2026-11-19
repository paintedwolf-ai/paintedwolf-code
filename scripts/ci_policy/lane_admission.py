"""Every CI lane's commands, in every profile it runs, pass the admission and package selection they meet when
they run, so the merge queue is never the first place a lane's own invocation is refused."""
import subprocess
import sys

import verification_plan
from ci_policy import hosted_plan
from ci_policy.hosted_execution import ROOT, lane_commands, recipe_tags
from ci_policy.impact import buildable

# The system each hosted runner family builds for.
RUNNER_SYSTEMS = {"macos": "darwin", "ubuntu": "linux"}


def runner_system(lane):
    runner = lane.get("runner", "ubuntu-24.04")
    return next(system for prefix, system in RUNNER_SYSTEMS.items() if runner.startswith(prefix))


def change_scopes():
    """No change scope, as a run outside a pull request or merge group has, and a change to every tracked file,
    which selects the most each lane can run."""
    paths = subprocess.check_output(["git", "ls-files"], cwd=ROOT, text=True).splitlines()
    return [None, {"base": None, "head": None, "full": False, "reasons": [], "paths": paths}]


def command_violations(command, goos):
    """Why one `./task` command would be refused, or name a Go package that does not build on its runner."""
    arguments = command[1:]
    separator = arguments.index("--") if "--" in arguments else len(arguments)
    names, cli = arguments[:separator], arguments[separator + 1:]
    try:
        verification_plan.request(arguments, {})
    except ValueError as error:
        return [str(error)]
    if len(names) != 1 or names[0] not in verification_plan.catalog()["go"] or not cli:
        return []
    packages = [package for package in verification_plan.go_selection(cli, names[0])["packages"]
                if not package.endswith("/...")]
    missing = sorted(set(packages) - set(buildable(packages, recipe_tags(names[0]), goos)))
    return [f"{names[0]} selects packages with no Go files on {goos}: {' '.join(missing)}"] if missing else []


def violations(scopes=None):
    scopes = change_scopes() if scopes is None else scopes
    found, judged = [], {}
    for name, lane in hosted_plan.lanes().items():
        goos = runner_system(lane)
        for profile in lane["profiles"]:
            if not hosted_plan.profile_invocations(lane, profile):
                continue
            for scope in scopes:
                # The planner gives the changed-package lane a change scope whenever it selects it.
                if scope is None and lane.get("phases") == "changed-packages":
                    continue
                label = f"{name} ({profile}, {'no scope' if scope is None else 'every path changed'})"
                try:
                    commands, _ = lane_commands(name, lane, profile, scope, goos)
                except ValueError as error:
                    found.append(f"{label}: {error}")
                    continue
                for command in commands:
                    if command[:1] != ["./task"]:
                        continue
                    key = (tuple(command), goos)
                    if key not in judged:
                        judged[key] = command_violations(command, goos)
                    found += [f"{label}: {' '.join(command)}: {reason}" for reason in judged[key]]
    return found


def main():
    found = violations()
    for line in found:
        print(f"lane admission: {line}", file=sys.stderr)
    return 1 if found else 0


if __name__ == "__main__":
    raise SystemExit(main())

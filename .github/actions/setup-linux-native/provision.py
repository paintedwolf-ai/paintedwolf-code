"""Resolve and install the native verification environment on an ephemeral Linux runner."""
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys

ROOT = Path(__file__).resolve().parent


def packages():
    names = (ROOT / "packages.txt").read_text().splitlines()
    if not names or len(names) != len(set(names)) or any(not re.fullmatch(r"[a-z0-9][a-z0-9+.-]*", n) for n in names):
        raise ValueError("native packages must be unique Debian package names")
    return names


def apt(*arguments, capture=False):
    result = subprocess.run(["sudo", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", *arguments],
                            check=True, text=True, stdout=subprocess.PIPE if capture else None)
    return result.stdout


def runner_image(environment):
    fields = ("ImageOS", "ImageVersion", "RUNNER_ARCH")
    missing = [name for name in fields if not environment.get(name)]
    if missing:
        raise ValueError("native package caching requires GitHub-hosted runner metadata: " + ", ".join(missing))
    return [environment[name] for name in fields]


def archive_identity(plan, names, environment):
    archives = []
    for line in plan.splitlines():
        if line.startswith("'"):
            fields = shlex.split(line)
            if len(fields) != 4 or not fields[2].isdigit() or not re.fullmatch(r"SHA256:[0-9a-fA-F]{64}", fields[3]):
                raise ValueError("APT archive plan must contain sizes and SHA256 digests")
            archives.append(fields[1:])
    identity = {"packages": names, "archives": sorted(archives),
                "image": runner_image(environment)}
    return "linux-native-v1-" + hashlib.sha256(json.dumps(identity, sort_keys=True).encode()).hexdigest()


def main(mode):
    if os.environ.get("RUNNER_OS") != "Linux":
        raise ValueError("native package preparation requires a Linux runner")
    runner_image(os.environ)
    names = packages()
    directory = Path(os.environ["RUNNER_TEMP"]) / "paintedwolf-native-archives"
    (directory / "partial").mkdir(parents=True, exist_ok=True)
    if mode == "plan":
        apt("update")
        plan = apt("-o", "Acquire::ForceHash=SHA256", "--print-uris", "--yes", "--download-only", "--no-install-recommends", "install", *names, capture=True)
        key = archive_identity(plan, names, os.environ)
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            output.write(f"archives={directory}\nkey={key}\n")
    elif mode == "install":
        apt("-o", f"Dir::Cache::archives={directory}", "-o", "APT::Keep-Downloaded-Packages=true",
            "--yes", "--no-install-recommends", "install", *names)
    else:
        raise ValueError("expected plan or install")


if __name__ == "__main__":
    main(sys.argv[1])

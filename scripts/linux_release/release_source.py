"""Read release authority facts from immutable Git objects without executing source."""

from __future__ import annotations

import hashlib
import os
from pathlib import Path
import re
import subprocess

from scripts.release_semver import parse, parse_release_build


def facts(repository: Path, source_commit: str) -> dict:
    if not isinstance(source_commit, str) or not re.fullmatch(r"[0-9a-f]{40}", source_commit):
        raise ValueError("publication source must be an exact commit SHA")
    environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    environment.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL="/dev/null")
    command = ["git", "--no-replace-objects", "-C", str(repository)]
    def git(*arguments):
        return subprocess.check_output([*command, *arguments], env=environment)
    if git("rev-parse", source_commit + "^{commit}").decode().strip() != source_commit:
        raise ValueError("publication source is not the selected commit object")
    def blob(name, limit):
        spec = source_commit + ":" + name
        entry = git("ls-tree", source_commit, "--", name)
        if not entry.startswith(b"100644 blob "):
            raise ValueError("publication source metadata must be a regular tracked file")
        if int(git("cat-file", "-s", spec)) > limit:
            raise ValueError("publication source metadata exceeds its size bound")
        return git("show", spec)
    version = blob("VERSION", 1024).decode().strip()
    product = parse(version)
    build = parse_release_build(blob("RELEASE_BUILD", 32).decode().strip())
    tree = git("rev-parse", source_commit + "^{tree}").decode().strip()
    result = {"source_commit": source_commit, "source_tree": tree,
              "version": version, "channel": product.channel, "release_build": build}
    lock = subprocess.run([*command, "cat-file", "-e", source_commit + ":flake.lock"], env=environment,
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if lock.returncode == 0:
        result["flake_lock_sha256"] = hashlib.sha256(blob("flake.lock", 1 << 20)).hexdigest()
    elif lock.returncode != 128:
        raise ValueError("could not inspect the selected source lock")
    return result

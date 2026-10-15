#!/usr/bin/env python3
"""Pin the last published stable release as the compatibility baseline."""

import json
import subprocess
import sys

from release_semver import compare, parse


def resolve(release, candidate_ref="HEAD"):
    if release.get("draft") is not False or release.get("prerelease") is not False:
        raise ValueError("compatibility baseline must be a published stable release")
    tag = release.get("tag_name", "")
    if (not isinstance(tag, str) or not tag.startswith("v")
            or not isinstance(release.get("published_at"), str) or not release["published_at"]):
        raise ValueError("published stable release identity is missing")
    version = parse(tag[1:])
    if version.major < 1 or version.prerelease is not None:
        raise ValueError("compatibility baseline must be post-v1 and stable")
    def git(*arguments):
        return subprocess.check_output(["git", *arguments], text=True).strip()
    sha = git("rev-parse", "--verify", "refs/tags/" + tag + "^{commit}")
    if git("show", sha + ":VERSION") != tag[1:]:
        raise ValueError("published stable tag does not match its source VERSION")
    subprocess.run(["git", "merge-base", "--is-ancestor", sha, candidate_ref], check=True)
    candidate = git("show", candidate_ref + ":VERSION")
    if compare(version, parse(candidate)) > 0:
        raise ValueError("candidate is older than the published stable release")
    if compare(version, parse(candidate)) == 0 and git("rev-parse", candidate_ref) != sha:
        raise ValueError("candidate reuses a published product version at another commit")
    return tag, sha


if __name__ == "__main__":
    try:
        tag, sha = resolve(json.load(sys.stdin), sys.argv[1] if len(sys.argv) > 1 else "HEAD")
    except (ValueError, TypeError, AttributeError, OSError, subprocess.CalledProcessError) as error:
        sys.exit("error: " + str(error))
    print("baseline_tag=" + tag)
    print("baseline_sha=" + sha)

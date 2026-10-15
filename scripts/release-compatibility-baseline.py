#!/usr/bin/env python3
"""Pin the last published stable release as the compatibility baseline."""

import argparse
import hashlib
import json
from pathlib import Path
import tempfile
import urllib.error
import urllib.request
import urllib.parse
import subprocess
import sys

from release_semver import compare, parse
from release_distribution import PUBLIC_READ_HEADERS, timestamp
import feed_signature
import feed_signing
from update_keys import load_registry, validate_binding


def stable_version(release):
    if release.get("draft") is not False or release.get("prerelease") is not False:
        raise ValueError("compatibility baseline must be a published stable release")
    tag = release.get("tag_name", "")
    if (not isinstance(tag, str) or not tag.startswith("v")
            or not isinstance(release.get("published_at"), str) or not release["published_at"]):
        raise ValueError("published stable release identity is missing")
    version = parse(tag[1:])
    if version.major < 1 or version.prerelease is not None:
        raise ValueError("compatibility baseline must be post-v1 and stable")
    return version


def resolve(release, candidate_ref="HEAD"):
    version = stable_version(release)
    tag = release["tag_name"]
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


def read_public(url):
    try:
        with urllib.request.urlopen(urllib.request.Request(url, headers=PUBLIC_READ_HEADERS), timeout=30) as response:
            raw = response.read(2 * 1024 * 1024 + 1)
            if len(raw) > 2 * 1024 * 1024:
                raise ValueError("published feed exceeds its size bound")
            return raw
    except urllib.error.HTTPError as error:
        if error.code == 404:
            return None
        raise


def published_stable(mirror, base):
    origin = urllib.parse.urlsplit(base)
    if (origin.scheme != "https" or not origin.netloc or origin.path not in ("", "/")
            or origin.username or origin.password or origin.query or origin.fragment):
        raise ValueError("stable baseline requires the configured HTTPS download origin")
    best, newest = mirror, stable_version(mirror)
    registry = load_registry()
    confirmed = False
    for row in registry["generations"]:
        url = f"{base.rstrip('/')}/updates/stable/key-{row['generation']}/latest.json"
        raw = read_public(url)
        if raw is None:
            continue
        signature = read_public(url + ".sig")
        if signature is None:
            raise ValueError("published stable feed signature is missing")
        manifest = json.loads(raw)
        validate_binding(registry, manifest, row["generation"])
        release = {"draft": False, "prerelease": False, "tag_name": "v" + manifest["version"],
                   "published_at": manifest["pub_date"], "feed_manifest_sha256": hashlib.sha256(raw).hexdigest()}
        version = stable_version(release)
        timestamp(manifest["pub_date"])
        with tempfile.TemporaryDirectory(prefix="release-stable-baseline-") as temporary:
            pointer = Path(temporary) / "latest.json"
            pointer.write_bytes(raw)
            feed_signature.check(signature.decode("utf-8"), file="latest.json", version=manifest["version"],
                                 number=row["generation"], registry=registry)
            feed_signing.verify(pointer, signature.decode("utf-8"), row["feed_public_key"])
        confirmed = True
        if compare(version, newest) >= 0:
            best, newest = release, version
    if not confirmed:
        raise ValueError("no signed published stable feed is available")
    return best


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source_ref", nargs="?", default="HEAD")
    parser.add_argument("--base-url", required=True)
    args = parser.parse_args()
    try:
        release = published_stable(json.load(sys.stdin), args.base_url)
        tag, sha = resolve(release, args.source_ref)
    except (ValueError, KeyError, TypeError, AttributeError, OSError, subprocess.CalledProcessError) as error:
        sys.exit("error: " + str(error))
    print("baseline_tag=" + tag)
    print("baseline_sha=" + sha)
    if "feed_manifest_sha256" in release:
        print("baseline_manifest_sha256=" + release["feed_manifest_sha256"])

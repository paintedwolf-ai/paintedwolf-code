#!/usr/bin/env python3
"""Pin the last published stable release as the compatibility baseline."""

import argparse
import hashlib
import json
from pathlib import Path
import tempfile
import re
import urllib.error
import urllib.request
import urllib.parse
import subprocess
import sys

from release_semver import compare, parse, parse_release_build
from release_distribution import PUBLIC_READ_HEADERS, timestamp
import feed_signature
import feed_signing
from update_keys import fingerprint, generation, load_registry, validate_binding


def published_version(release, channel):
    if release.get("draft") is not False or release.get("prerelease") is not (channel == "preview"):
        raise ValueError("release must be published in the " + channel + " channel")
    tag = release.get("tag_name", "")
    if (not isinstance(tag, str) or not tag.startswith("v")
            or not isinstance(release.get("published_at"), str) or not release["published_at"]):
        raise ValueError("published stable release identity is missing")
    version = parse(tag[1:])
    if version.major < 1 or version.channel != channel:
        raise ValueError("release must be post-v1 and in the " + channel + " channel")
    timestamp(release["published_at"])
    return version


def stable_version(release):
    return published_version(release, "stable")


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


def published_feed(mirror, base, channel):
    origin = urllib.parse.urlsplit(base)
    if (origin.scheme != "https" or not origin.netloc or origin.path not in ("", "/")
            or origin.username or origin.password or origin.query or origin.fragment):
        raise ValueError("stable baseline requires the configured HTTPS download origin")
    best = mirror
    newest = published_version(mirror, channel) if mirror is not None else None
    registry = load_registry()
    confirmed = False
    for row in registry["generations"]:
        url = f"{base.rstrip('/')}/updates/{channel}/key-{row['generation']}/latest.json"
        raw = read_public(url)
        if raw is None:
            continue
        signature = read_public(url + ".sig")
        if signature is None:
            raise ValueError("published stable feed signature is missing")
        manifest = json.loads(raw)
        validate_binding(registry, manifest, row["generation"])
        release = {"draft": False, "prerelease": channel == "preview", "tag_name": "v" + manifest["version"],
                   "published_at": manifest["pub_date"], "feed_manifest_sha256": hashlib.sha256(raw).hexdigest()}
        version = published_version(release, channel)
        with tempfile.TemporaryDirectory(prefix="release-stable-baseline-") as temporary:
            pointer = Path(temporary) / "latest.json"
            pointer.write_bytes(raw)
            feed_signature.check(signature.decode("utf-8"), file="latest.json", version=manifest["version"],
                                 number=row["generation"], registry=registry)
            feed_signing.verify(pointer, signature.decode("utf-8"), row["feed_public_key"])
        confirmed = True
        if newest is None or compare(version, newest) >= 0:
            best, newest = release, version
    if not confirmed and channel == "stable":
        raise ValueError("no signed published stable feed is available")
    return best


def published_stable(mirror, base):
    return published_feed(mirror, base, "stable")


def package_versions(raw, repeated):
    table = r"\[\[package\]\]" if repeated else r"\[package\]"
    sections = re.findall(r"(?ms)^" + table + r"[^\S\n]*\n(.*?)(?=^\[|\Z)", raw)
    versions = []
    for section in sections:
        def field(name):
            values = re.findall(r"(?m)^" + name + r"\s*=\s*(\"[^\"\n]*\")[^\S\n]*(?:#.*)?$", section)
            if len(values) != 1:
                raise ValueError("release package requires one quoted " + name)
            return json.loads(values[0])
        if not repeated or field("name") == "painted-wolf-code":
            versions.append(field("version"))
    return versions


def check_source_versions(source_ref):
    def read(path):
        return subprocess.check_output(["git", "show", source_ref + ":" + path], text=True).strip()
    product = read("VERSION")
    version = parse(product)
    build = str(parse_release_build(read("RELEASE_BUILD")))
    package = json.loads(read("lycaon-den/package.json"))
    tauri = json.loads(read("lycaon-den/src-tauri/tauri.conf.json"))
    cargo = package_versions(read("lycaon-den/src-tauri/Cargo.toml"), repeated=False)
    locked = package_versions(read("lycaon-den/src-tauri/Cargo.lock"), repeated=True)
    if (package["version"] != product or tauri["version"] != version.core
            or cargo != [version.core] or locked != [version.core]
            or tauri["bundle"]["macOS"]["bundleVersion"] != build):
        raise ValueError("packaging versions or native build do not match VERSION and RELEASE_BUILD")
    changelog = read("CHANGELOG.md")
    headings = list(re.finditer(r"(?m)^## \[" + re.escape(product) + r"\][^\n]*\n", changelog))
    if len(headings) != 1 or not changelog[headings[0].end():].split("\n## [", 1)[0].strip():
        raise ValueError("candidate has no release notes")
    return product


def check_progression(candidate, releases, channel_release, source_ref):
    selected = parse(candidate)
    if selected.major < 1 or "+" in candidate:
        raise ValueError("release VERSION must be post-v1 without build metadata")
    if channel_release is not None and compare(selected, published_version(channel_release, selected.channel)) <= 0:
        raise ValueError("release must be newer than the last publication in its channel")
    candidate_build = parse_release_build(subprocess.check_output(
        ["git", "show", source_ref + ":RELEASE_BUILD"], text=True).strip())
    for release in releases:
        if release.get("draft") is True:
            continue
        channel = "preview" if release.get("prerelease") is True else "stable"
        prior = published_version(release, channel)
        if compare(selected, prior) == 0:
            raise ValueError("release version has already been published")
        raw = subprocess.check_output(["git", "show", "refs/tags/" + release["tag_name"] + ":RELEASE_BUILD"], text=True).strip()
        if candidate_build <= parse_release_build(raw):
            raise ValueError("RELEASE_BUILD must exceed every published release build")


def check_key_history(previous, current):
    for counter in ("signing_generation", "embedded_generation"):
        if current[counter] < previous[counter]:
            raise ValueError("published signing generation regressed")
    for prior in previous["generations"]:
        retained = generation(current, prior["generation"])
        for field in ("public_key", "feed_public_key"):
            if field in prior and fingerprint(prior[field]) != fingerprint(retained[field]):
                raise ValueError("published signing key changed within its generation")
        if prior["successor"] is not None and any(retained[field] != prior[field] for field in ("successor", "bridge_version")):
            raise ValueError("published signing bridge changed")


def check_published_keys(source_ref, releases):
    def registry(ref):
        return subprocess.check_output(["git", "show", ref + ":packaging/update-keys.json"], text=True)
    with tempfile.TemporaryDirectory(prefix="release-key-history-") as temporary:
        path = Path(temporary) / "update-keys.json"
        path.write_text(registry(source_ref))
        current = load_registry(path)
    for release in releases:
        if release.get("draft") is False:
            previous = json.loads(registry("refs/tags/" + release["tag_name"]))
            check_key_history(previous, current)


def channel_mirror(releases, channel):
    selected, newest = None, None
    for release in releases:
        if release.get("draft") is True:
            continue
        version = published_version(release, "preview" if release.get("prerelease") is True else "stable")
        if version.channel == channel and (newest is None or compare(version, newest) > 0):
            selected, newest = release, version
    return selected


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source_ref", nargs="?", default="HEAD")
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--releases-file", type=Path, required=True)
    args = parser.parse_args()
    try:
        candidate = check_source_versions(args.source_ref)
        release = published_stable(json.load(sys.stdin), args.base_url)
        tag, sha = resolve(release, args.source_ref)
        pages = json.loads(args.releases_file.read_text())
        releases = [row for page in pages for row in page]
        channel = parse(candidate).channel
        preview = published_feed(channel_mirror(releases, "preview"), args.base_url, "preview")
        latest = release if channel == "stable" else preview
        history = releases + [release] + ([preview] if preview is not None else [])
        check_progression(candidate, history, latest, args.source_ref)
        check_published_keys(args.source_ref, history)
    except (ValueError, KeyError, TypeError, AttributeError, OSError, subprocess.CalledProcessError) as error:
        sys.exit("error: " + str(error))
    print("baseline_tag=" + tag)
    print("baseline_sha=" + sha)
    versions = sorted({row["tag_name"][1:] for row in history if row.get("draft") is False
                       and compare(parse(row["tag_name"][1:]), parse("1.0.0")) >= 0})
    print("published_versions=" + json.dumps(versions, separators=(",", ":")))
    if "feed_manifest_sha256" in release:
        print("baseline_manifest_sha256=" + release["feed_manifest_sha256"])

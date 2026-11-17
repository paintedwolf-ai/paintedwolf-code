"""Published upgrade-source selection."""
from __future__ import annotations

import argparse
from functools import cmp_to_key
import json
from pathlib import Path
import tempfile

from release_semver import compare, parse

FIRST_RELEASE = parse("1.0.0")

def prior_versions(feeds: dict[str, dict | None], candidate: str) -> list[str]:
    target = parse(candidate)
    versions = set()
    for channel, manifest in feeds.items():
        if manifest is None:
            continue
        version = manifest["version"]
        parsed = parse(version)
        if channel == "stable" and parsed.channel != "stable":
            raise ValueError("Stable feed contains a prerelease")
        # Only releases from FIRST_RELEASE on are upgrade sources.
        if compare(parsed, target) < 0 and compare(parsed, FIRST_RELEASE) >= 0:
            versions.add(version)
    # Withdrawn releases remain upgrade sources for existing installations.
    return sorted(versions)


def capture_sources(base: str, candidate: dict, destination: Path) -> dict:
    from release_upgrade.baseline import capture as capture_baseline
    from release_upgrade.feeds import capture
    from release_upgrade.plan import plan
    from update_keys import load_registry
    if base != "https://downloads.paintedwolf.dev":
        raise ValueError("upgrade rehearsal requires the production HTTPS origin")
    registry = load_registry()
    destination.mkdir(parents=True, exist_ok=True)
    snapshots = []
    for row in registry["generations"]:
        if row["generation"] > candidate["update_keys"]["signing_generation"]:
            continue
        for channel in ("stable", "preview"):
            snapshot = capture(base, channel, row["generation"], registry, destination)
            if snapshot is not None:
                snapshots.append(snapshot)
    anchors = capture_baseline(base, candidate["version"], registry, snapshots, destination)
    result = plan(candidate, registry, snapshots, anchors)
    result.update(origin=base, candidate_source="release")
    (destination / "plan.json").write_text(json.dumps(result, indent=2) + "\n")
    return result


def capture_published(base: str, source: Path, destination: Path) -> dict:
    """Plan the newest published release's upgrade from the published release before it.

    Every input is public and already signed with production keys: the channel pointers
    and the older release's retained manifest. The source's upgrade corpus names the
    releases that can be sources.
    """
    from release_upgrade.baseline import retained
    from release_upgrade.feeds import capture
    from release_upgrade.plan import client_protocol, plan
    from update_keys import load_registry
    if base != "https://downloads.paintedwolf.dev":
        raise ValueError("upgrade rehearsal requires the production HTTPS origin")
    registry = load_registry()
    destination.mkdir(parents=True, exist_ok=True)
    snapshots = [snapshot for row in registry["generations"] for channel in ("stable", "preview")
                 if (snapshot := capture(base, channel, row["generation"], registry, destination)) is not None]
    if not snapshots:
        raise ValueError("no release is published")
    newest = snapshots[0]
    for snapshot in snapshots[1:]:
        if compare(parse(snapshot["manifest"]["version"]), parse(newest["manifest"]["version"])) > 0:
            newest = snapshot
    candidate = newest["manifest"]
    corpus = [path.name for path in (source / "lycaon/testdata/upgrade-corpus").iterdir() if path.is_dir()]
    earlier = sorted((version for version in corpus if compare(parse(version), parse(candidate["version"])) < 0
                      and compare(parse(version), FIRST_RELEASE) >= 0 and client_protocol(version) == "native-v2"),
                     key=cmp_to_key(lambda left, right: compare(parse(left), parse(right))), reverse=True)
    # A corpus is sealed before its tag, so the newest one with a retained manifest is the published one.
    previous = next((anchor for version in earlier if (anchor := retained(base, version, registry, destination))), None)
    if previous is None:
        raise ValueError("no earlier published release has an automatic updater")
    result = plan(candidate, registry, snapshots, [previous])
    result.update(origin=base, candidate_source="published")
    (destination / "plan.json").write_text(json.dumps(result, indent=2) + "\n")
    return result


def resolve_prior(base: str, candidate: str, generation: int) -> list[str]:
    from release_upgrade.feeds import capture
    from update_keys import load_registry
    if base != "https://downloads.paintedwolf.dev" or generation < 1:
        raise ValueError("upgrade rehearsal requires the production HTTPS origin and positive key generation")
    feeds = {}
    with tempfile.TemporaryDirectory(prefix="release-prior-") as temporary:
        for channel in ("stable", "preview"):
            snapshot = capture(base, channel, generation, load_registry(), Path(temporary))
            feeds[channel] = snapshot["manifest"] if snapshot else None
    return prior_versions(feeds, candidate)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    prior = commands.add_parser("prior")
    prior.add_argument("--base-url", required=True)
    prior.add_argument("--candidate", required=True)
    prior.add_argument("--generation", type=int, required=True)
    snapshot = commands.add_parser("snapshot")
    snapshot.add_argument("--base-url", required=True)
    snapshot.add_argument("--candidate-manifest", type=Path, required=True)
    snapshot.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.command == "snapshot":
        args.output.mkdir(parents=True, exist_ok=False)
        capture_sources(args.base_url, json.loads(args.candidate_manifest.read_text()), args.output)
        return
    print("\n".join("v" + version for version in resolve_prior(args.base_url, args.candidate, args.generation)))


if __name__ == "__main__":
    import importlib
    import sys
    handlers = {"bundle-prepare": "prepare", "bundle-rehearse": "rehearse", "bundle-serve": "server"}
    selected = handlers.get(sys.argv[1] if len(sys.argv) > 1 else "")
    if selected:
        del sys.argv[1]
        importlib.import_module("release_upgrade." + selected).main()
    else:
        main()

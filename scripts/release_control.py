"""Published upgrade-source selection."""
from __future__ import annotations

import argparse
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
    from release_upgrade_feeds import capture
    from release_upgrade_plan import plan
    from update_keys import load_registry
    if base != "https://downloads.paintedwolf.dev":
        raise ValueError("upgrade rehearsal requires the production HTTPS origin")
    registry = load_registry()
    snapshots = []
    for row in registry["generations"]:
        if row["generation"] > candidate["update_keys"]["signing_generation"]:
            continue
        for channel in ("stable", "preview"):
            snapshot = capture(base, channel, row["generation"], registry, destination)
            if snapshot is not None:
                snapshots.append(snapshot)
    result = plan(candidate, registry, snapshots)
    result["origin"] = base
    (destination / "plan.json").write_text(json.dumps(result, indent=2) + "\n")
    return result


def resolve_prior(base: str, candidate: str, generation: int) -> list[str]:
    from release_upgrade_feeds import capture
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
    main()

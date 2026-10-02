"""Release event admission and published upgrade-source selection."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import urllib.error
import urllib.request

from release_distribution import PUBLIC_READ_HEADERS
from release_semver import compare, parse

FIRST_RELEASE = parse("1.0.0")

ROOT = Path(__file__).resolve().parent.parent


def publishes(event: str, ref_type: str) -> bool:
    if event == "workflow_dispatch":
        return False
    if event == "push" and ref_type == "tag":
        return True
    raise ValueError("release requires a manual rehearsal or a tag push")


def read_public(url: str) -> dict | None:
    request = urllib.request.Request(url, headers=PUBLIC_READ_HEADERS)
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            raw = response.read(2 * 1024 * 1024 + 1)
            if len(raw) > 2 * 1024 * 1024:
                raise ValueError("release metadata exceeds its size bound")
            return json.loads(raw)
    except urllib.error.HTTPError as error:
        if error.code == 404:
            return None
        raise


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
        # Pre-v1 stores were never released baselines, so they are not upgrade sources.
        if compare(parsed, target) < 0 and compare(parsed, FIRST_RELEASE) >= 0:
            versions.add(version)
    # Withdrawn releases remain upgrade sources for existing installations.
    return sorted(versions)


def resolve_prior(base: str, candidate: str, generation: int) -> list[str]:
    if not base.startswith("https://") or base.endswith("/") or generation < 1:
        raise ValueError("upgrade rehearsal requires the configured HTTPS download origin and positive key generation")
    feeds = {}
    for channel in ("stable", "preview"):
        manifest = read_public(f"{base}/updates/{channel}/key-{generation}/latest.json")
        if manifest is not None:
            with tempfile.TemporaryDirectory(prefix="release-prior-") as temporary:
                path = Path(temporary) / "manifest.json"
                path.write_text(json.dumps(manifest))
                subprocess.run(["bash", str(ROOT / "scripts/release-validate-updater-manifest.sh"), "--file", str(path), "--existing"],
                               env={**os.environ, "DOWNLOAD_BASE_URL": base}, check=True)
            if manifest["update_keys"]["signing_generation"] != generation:
                raise ValueError("prior manifest belongs to another signing generation")
        feeds[channel] = manifest
    return prior_versions(feeds, candidate)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("classify")
    prior = commands.add_parser("prior")
    prior.add_argument("--base-url", required=True)
    prior.add_argument("--candidate", required=True)
    prior.add_argument("--generation", type=int, required=True)
    args = parser.parse_args()
    if args.command == "classify":
        print(str(publishes(os.environ["GITHUB_EVENT_NAME"], os.environ["GITHUB_REF_TYPE"])).lower())
    else:
        print("\n".join("v" + version for version in resolve_prior(args.base_url, args.candidate, args.generation)))


if __name__ == "__main__":
    main()

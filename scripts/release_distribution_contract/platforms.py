"""Select package and updater surfaces without changing publication authority."""
from __future__ import annotations

import json
from pathlib import Path
import re

from release_semver import parse

ORIGIN = "https://downloads.paintedwolf.dev"
LEGACY_PLATFORMS = Path(__file__).resolve().parents[2] / "packaging/distribution-legacy-platforms.json"


def load_platforms(path: Path) -> dict[str, dict]:
    return parse_platforms(path.read_bytes())


def parse_platforms(raw: bytes) -> dict[str, dict]:
    catalog = json.loads(raw)
    if not isinstance(catalog, dict):
        raise ValueError("release platform catalog must be an object")
    rows = catalog.get("platforms")
    if type(catalog.get("schema_version")) is not int or catalog["schema_version"] != 2 or not isinstance(rows, list) or not rows:
        raise ValueError("release platform catalog has an unsupported shape")
    result = {}
    extension_pattern = r"[A-Za-z0-9]+(?:\.[A-Za-z0-9]+)*"
    for row in rows:
        if not isinstance(row, dict):
            raise ValueError("release platform catalog entry must be an object")
        key = row.get("updater_key")
        if not isinstance(key, str) or not re.fullmatch(r"[a-z0-9]+-[a-z0-9_]+", key):
            raise ValueError("release platform catalog has an invalid updater key")
        if key in result:
            raise ValueError("release platform catalog repeats an updater key")
        if row.get("publication") not in {"public", "candidate"}:
            raise ValueError(f"release platform {key} has an invalid publication state")
        packages = row.get("package_extensions")
        if not isinstance(packages, list) or not packages or any(
            not isinstance(value, str) or not re.fullmatch(extension_pattern, value)
            for value in packages
        ) or len(set(packages)) != len(packages):
            raise ValueError(f"release platform {key} has invalid package extensions")
        if "updater_extension" not in row:
            raise ValueError(f"release platform {key} must declare its updater ownership")
        updater = row["updater_extension"]
        if updater is not None and (not isinstance(updater, str) or not re.fullmatch(extension_pattern, updater)):
            raise ValueError(f"release platform {key} has an invalid updater extension")
        result[key] = row
    if not any(row["publication"] == "public" for row in result.values()):
        raise ValueError("release platform catalog has no public platform")
    return result


def validate_release_catalog(raw: bytes) -> dict[str, dict]:
    """Validate shipping inputs while keeping package publication separate from updates."""
    platforms = parse_platforms(raw)
    fields = {'artifact_arch', 'goarch', 'goos', 'package_extensions', 'publication',
              'runner', 'updater_extension', 'updater_key'}
    identities = {
        'darwin-aarch64': ('darwin', 'arm64', 'aarch64'),
        'linux-x86_64': ('linux', 'amd64', 'x86_64'),
        'linux-aarch64': ('linux', 'arm64', 'aarch64'),
        'windows-x86_64': ('windows', 'amd64', 'x86_64'),
    }
    if set(platforms) != set(identities):
        raise ValueError('release platform set differs from the supported adapters')
    linux = {'linux-aarch64', 'linux-x86_64'}
    for key, row in platforms.items():
        if (set(row) != fields or not isinstance(row['runner'], str) or not row['runner']
                or (row['goos'], row['goarch'], row['artifact_arch']) != identities[key]):
            raise ValueError('release platform execution fields are invalid')
        if row['goos'] == 'linux':
            if row['package_extensions'] != ['deb', 'rpm', 'pkg.tar.zst'] or row['updater_extension'] is not None:
                raise ValueError('native Linux requires every package form and no automatic updater')
        elif row['publication'] == 'public' and row['updater_extension'] is None:
            raise ValueError('public non-Linux platform requires its automatic updater')
    public = {key for key, row in platforms.items() if row['publication'] == 'public'}
    if public not in ({'darwin-aarch64'}, {'darwin-aarch64', *linux}):
        raise ValueError('release must retain macOS and publish both Linux architectures together')
    return platforms


def detached_signatures(row: dict) -> list[str]:
    """Linux packages other than RPM carry a detached OpenPGP signature; RPM embeds its own."""
    if row.get("goos") != "linux":
        return []
    return [extension for extension in row["package_extensions"] if extension != "rpm"]


def public_updaters(platforms: dict[str, dict]) -> dict[str, dict]:
    return {key: row for key, row in platforms.items()
            if row["publication"] == "public" and row["updater_extension"] is not None}


def package_urls(version: str, catalog: Path, *, origin: str = ORIGIN) -> list[str]:
    parse(version)
    return sorted(
        f"{origin}/releases/v{version}/painted-wolf-code_v{version}_{key}.{extension}"
        for key, row in load_platforms(catalog).items() if row["publication"] == "public"
        for extension in row["package_extensions"]
    )

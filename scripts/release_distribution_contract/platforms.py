"""Select package and updater surfaces without changing publication authority."""
from __future__ import annotations

import json
from pathlib import Path
import re

from release_semver import parse


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


def public_updaters(platforms: dict[str, dict]) -> dict[str, dict]:
    return {key: row for key, row in platforms.items()
            if row["publication"] == "public" and row["updater_extension"] is not None}


def package_urls(version: str, catalog: Path) -> list[str]:
    parse(version)
    return sorted(
        f"https://downloads.paintedwolf.dev/releases/v{version}/painted-wolf-code_v{version}_{key}.{extension}"
        for key, row in load_platforms(catalog).items() if row["publication"] == "public"
        for extension in row["package_extensions"]
    )

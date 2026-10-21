"""Attribute every store path of a redistributed Nix closure to its package licence and source."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import sys

STORE_PATH = re.compile(r"/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[^/\s]+\Z")
# Licences whose binary redistribution carries a corresponding-source obligation.
COPYLEFT = re.compile(r"^(A?GPL|LGPL|MPL|EPL|EUPL|CDDL|OSL|CECILL)", re.IGNORECASE)


def copyleft(licence: dict) -> bool:
    return any(isinstance(value, str) and COPYLEFT.match(value) for value in (licence.get("spdx"), licence.get("name")))


def attribute(catalog: dict, store_paths: list[str]) -> dict:
    """Map each closure path to exactly one described package; unknown or unfree paths refuse."""
    if catalog.get("format_version") != 1 or not isinstance(catalog.get("packages"), list):
        raise ValueError("closure catalog has an unsupported format")
    owners = {}
    for package in catalog["packages"]:
        for output in package.get("outputs", []):
            owners.setdefault(output, package)
    entries, unknown = [], []
    for path in store_paths:
        if not STORE_PATH.fullmatch(path):
            raise ValueError(f"closure lists an invalid store path: {path!r}")
        package = owners.get(path)
        if package is None:
            unknown.append(path)
            continue
        licences = package.get("licenses") or []
        if not licences:
            raise ValueError(f"{package['name']} ({path}) declares no licence")
        if any(licence.get("free") is False and licence.get("redistributable") is not True for licence in licences):
            raise ValueError(f"{package['name']} ({path}) is not redistributable")
        source = package.get("source")
        obligated = any(copyleft(licence) for licence in licences)
        if obligated and not (source and source.get("store_path") and source.get("output_hash")):
            raise ValueError(f"{package['name']} ({path}) is copyleft without a fixed corresponding source")
        entries.append({"store_path": path, "name": package["name"], "version": package.get("version"),
                        "licenses": licences, "homepage": package.get("homepage"),
                        "corresponding_source": source if obligated else None,
                        "nixpkgs_position": package.get("nixpkgs_position")})
    if unknown:
        raise ValueError("closure paths have no described package: " + ", ".join(sorted(unknown)))
    return {"format_version": 1, "application": catalog["application"],
            "nixpkgs_revision": catalog["nixpkgs_revision"], "source_commit": catalog.get("source_commit"),
            "paths": entries}


def markdown(notices: dict) -> str:
    lines = ["# Nix closure notices", "",
             f"Every store path of `{notices['application']}`, as built from nixpkgs "
             f"`{notices['nixpkgs_revision']}`. Copyleft components list their fixed corresponding source, "
             "which is published beside the binary closure and reproducible from that nixpkgs revision.", "",
             "| Store path | Package | Licences | Corresponding source |", "|---|---|---|---|"]
    for entry in notices["paths"]:
        names = ", ".join(licence.get("spdx") or licence.get("name") or "unknown" for licence in entry["licenses"])
        source = entry["corresponding_source"]
        offered = f"`{source['store_path']}` ({source['output_hash']})" if source else ""
        version = f" {entry['version']}" if entry["version"] else ""
        lines.append(f"| `{entry['store_path']}` | {entry['name']}{version} | {names} | {offered} |")
    return "\n".join(lines) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--catalog", type=Path, required=True)
    parser.add_argument("--store-paths", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    try:
        notices = attribute(json.loads(args.catalog.read_text()), args.store_paths.read_text().split())
    except ValueError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    args.out.mkdir(parents=True)
    (args.out / "closure-notices.json").write_text(json.dumps(notices, indent=2) + "\n")
    (args.out / "CLOSURE-NOTICES.md").write_text(markdown(notices))
    sources = sorted({entry["corresponding_source"]["store_path"] for entry in notices["paths"]
                      if entry["corresponding_source"]})
    (args.out / "corresponding-sources").write_text("".join(path + "\n" for path in sources))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

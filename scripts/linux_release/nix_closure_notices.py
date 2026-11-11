"""Attribute every store path of a redistributed Nix closure and resolve native scanner build tools."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import platform
import re
import subprocess
import sys

PNAME = "painted-wolf-code-scanner-build-tools"
STORE = r"/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[A-Za-z0-9+._?=-]+"
SYSTEMS = {"x86_64": "x86_64-linux", "aarch64": "aarch64-linux"}
STORE_PATH = re.compile(r"/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[^/\s]+\Z")
# Licences whose binary redistribution carries a corresponding-source obligation.
COPYLEFT = re.compile(r"^(A?GPL|LGPL|MPL|EPL|EUPL|CDDL|OSL|CECILL)", re.IGNORECASE)


def scanner_derivation(graph: dict, root: str, system: str) -> dict:
    if (system not in SYSTEMS.values() or not isinstance(graph, dict)
            or set(graph) != {"version", "derivations"} or graph["version"] != 4
            or not isinstance(graph["derivations"], dict)
            or not 0 < len(graph["derivations"]) <= 100000):
        raise ValueError("scanner selection requires the complete schema-4 native derivation graph")
    rows = graph["derivations"]
    root = Path(root).name
    if root not in rows or rows[root].get("system") != system:
        raise ValueError("application derivation is not for the selected native system")
    pending, visited, matches = [root], set(), []
    while pending:
        path = pending.pop()
        if path in visited:
            continue
        visited.add(path)
        if not isinstance(path, str) or re.fullmatch(STORE.removeprefix("/nix/store/") + r"\.drv", path) is None:
            raise ValueError("derivation graph contains an invalid store identity: " + repr(path)[:256])
        row = rows.get(path)
        if not isinstance(row, dict) or row.get("version") != 4 or not isinstance(row.get("inputs", {}).get("drvs"), dict):
            raise ValueError("derivation graph is incomplete")
        pending.extend(row["inputs"]["drvs"])
        environment = row.get("env")
        if not isinstance(environment, dict):
            raise ValueError("derivation graph omitted its typed environment")
        if environment.get("pname") != PNAME:
            continue
        output = row.get("outputs", {}).get("out", {})
        value = output.get("path")
        if isinstance(value, str):
            value = "/nix/store/" + value
        if (row.get("system") != system or not isinstance(value, str)
                or re.fullmatch(STORE, value) is None
                or not Path(value).name[33:].startswith(PNAME + "-")
                or environment.get("out") != value):
            raise ValueError("scanner tools do not have one fixed native output identity")
        matches.append({"derivation": "/nix/store/" + path, "output": value,
                        "executable": value + "/bin/opengrep-artifact", "system": system})
    if len(matches) != 1:
        raise ValueError("selected application must depend on exactly one native scanner build tool")
    return matches[0]


def command(nix, source, environment):
    if platform.system() != "Linux" or platform.machine() not in SYSTEMS:
        raise ValueError("scanner preparation requires a supported native Linux host")
    system = SYSTEMS[platform.machine()]
    selected = source + "#packages." + system + ".default"
    result = subprocess.run(nix + ["eval", "--raw", "--no-write-lock-file", selected + ".drvPath"],
                            env=environment, text=True, stdout=subprocess.PIPE, check=True)
    root = result.stdout.strip()
    result = subprocess.run(nix + ["derivation", "show", "--recursive", root],
                            env=environment, text=True, stdout=subprocess.PIPE, check=True)
    metadata = scanner_derivation(json.loads(result.stdout), root, system)
    invocation = nix + ["build", "--json", "--no-link", "--no-write-lock-file",
                        "--option", "sandbox", "true", "--max-jobs", "1", "--cores", "1",
                        metadata["derivation"] + "^out"]
    return invocation, metadata


def copyleft(licence: dict) -> bool:
    return any(isinstance(value, str) and COPYLEFT.match(value) for value in (licence.get("spdx"), licence.get("name")))


def attribute(catalog: dict, store_paths: list[str]) -> dict:
    """Map each closure path to exactly one described package; unknown or unfree paths refuse."""
    if catalog.get("format_version") != 2 or not isinstance(catalog.get("packages"), list):
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
        sources = package.get("sources", [])
        obligated = any(copyleft(licence) for licence in licences)
        if obligated and not (isinstance(sources, list) and sources and all(
                isinstance(source, dict) and isinstance(source.get("store_path"), str)
                and STORE_PATH.fullmatch(source["store_path"])
                and isinstance(source.get("output_hash"), str) and source["output_hash"] for source in sources)):
            raise ValueError(f"{package['name']} ({path}) is copyleft without a fixed corresponding source")
        entries.append({"store_path": path, "name": package["name"], "version": package.get("version"),
                        "licenses": licences, "homepage": package.get("homepage"),
                        "corresponding_sources": sources if obligated else [],
                        "nixpkgs_position": package.get("nixpkgs_position")})
    if unknown:
        raise ValueError("closure paths have no described package: " + ", ".join(sorted(unknown)))
    return {"format_version": 2, "application": catalog["application"],
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
        offered = ", ".join(f"`{source['store_path']}` ({source['output_hash']})"
                            for source in entry["corresponding_sources"])
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
    sources = sorted({source["store_path"] for entry in notices["paths"]
                      for source in entry["corresponding_sources"]})
    (args.out / "corresponding-sources").write_text("".join(path + "\n" for path in sources))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

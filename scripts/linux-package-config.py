#!/usr/bin/env python3
"""Emit the Tauri bundle config fragment that stages the Linux package tree.

The fragment carries what cannot live in the static Tauri config: dependency
constraints rendered from the measured Linux floor, and the `files` map that
places the engine payload under the package install root and wires the
optional packaging fragments other subsystems ship (polkit, systemd, AppArmor,
SELinux). Tauri stages that tree through its .deb target; linux-package-assemble.py
finishes both families from it. Missing optional directories are tolerated;
required payloads are not.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys

INSTALL_ROOT = "/opt/painted-wolf-code"
PACKAGE_NAME = "painted-wolf-code"
APP_ID = "dev.paintedwolf.code"
HELPERS = ("pw", "pw-logs", "pw-document-core", "bialy")
# Shipped when the build stages it; staging requires it once a unit runs it.
OPTIONAL_HELPERS = ("pw-vault-identity",)
ENTRY_POINTS = ("pw", "pw-logs")

# Optional packaging fragments: repository directory -> (destination directory,
# accepted suffixes, package families).
OPTIONAL_FRAGMENTS = {
    "polkit": ("/usr/share/polkit-1/actions", (".policy",), ("deb", "rpm")),
    "systemd": ("/usr/lib/systemd/system", (".service", ".socket", ".timer"), ("deb", "rpm")),
    "apparmor": ("/etc/apparmor.d", ("",), ("deb",)),
    "selinux": ("/usr/share/selinux/packages", (".pp",), ("rpm",)),
}


def floor_values(path: Path) -> dict[str, str]:
    values: dict[str, str] = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line:
            continue
        key, _, value = line.partition(" ")
        if not key or not value.strip():
            raise ValueError(f"malformed floor line in {path}: {line!r}")
        values[key] = value.strip()
    for required in ("kernel", "glibc", "webkitgtk", "gtk3"):
        if required not in values:
            raise ValueError(f"{path} lacks the {required} floor")
    return values


def render_dependencies(catalog: dict, family: str, floors: dict[str, str]) -> dict[str, list[str]]:
    rendered: dict[str, list[str]] = {}
    for field in ("depends", "recommends"):
        rows = catalog[family].get(field, [])
        if not isinstance(rows, list) or not all(isinstance(row, str) and row.strip() for row in rows):
            raise ValueError(f"dependencies.json {family}.{field} must be a list of non-empty strings")
        rendered[field] = [row.format(**floors) for row in rows]
    if not rendered["depends"]:
        raise ValueError(f"dependencies.json declares no {family} dependencies")
    return rendered


def required_files(root: Path, tauri_dir: Path, target: str, manifest: Path) -> dict[str, Path]:
    files: dict[str, Path] = {}
    for helper in HELPERS:
        files[f"{INSTALL_ROOT}/bin/{helper}"] = tauri_dir / "binaries" / f"{helper}-{target}"
    for helper in OPTIONAL_HELPERS:
        source = tauri_dir / "binaries" / f"{helper}-{target}"
        if source.exists():
            files[f"{INSTALL_ROOT}/bin/{helper}"] = source
    files[f"{INSTALL_ROOT}/engine-root"] = tauri_dir / "engine-root"
    files[f"{INSTALL_ROOT}/release-version"] = root / "VERSION"
    files[f"{INSTALL_ROOT}/THIRD-PARTY-NOTICES.md"] = tauri_dir / "THIRD-PARTY-NOTICES.md"
    files[f"{INSTALL_ROOT}/manifest.json"] = manifest
    for entry in ENTRY_POINTS:
        files[f"/usr/bin/{entry}"] = root / "packaging" / "linux" / "bin" / entry
    files[f"/usr/share/applications/{APP_ID}.desktop"] = root / "packaging" / "linux" / f"{APP_ID}.desktop"
    for destination, source in files.items():
        if not source.exists():
            raise ValueError(f"missing package payload for {destination}: {source}")
    return files


def optional_files(root: Path, family: str) -> dict[str, Path]:
    files: dict[str, Path] = {}
    linux = root / "packaging" / "linux"
    for fragment, (destination, suffixes, families) in OPTIONAL_FRAGMENTS.items():
        directory = linux / fragment
        if family not in families or not directory.is_dir():
            continue
        for source in sorted(directory.iterdir()):
            if not source.is_file() or source.name.startswith("."):
                continue
            if not any(source.name.endswith(suffix) for suffix in suffixes):
                raise ValueError(f"{source} is not a {fragment} fragment ({', '.join(suffixes)})")
            files[f"{destination}/{source.name}"] = source
    overrides = linux / "deb" / "lintian-overrides"
    if family == "deb" and overrides.is_file():
        files[f"/usr/share/lintian/overrides/{PACKAGE_NAME}"] = overrides
    return files


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parent.parent)
    parser.add_argument("--target", help="Rust target triple of the build")
    parser.add_argument("--manifest", type=Path, help="generated manifest.json")
    parser.add_argument(
        "--family-only",
        choices=("deb", "rpm"),
        help="print the installed paths only this package family ships, one per line",
    )
    args = parser.parse_args()
    root = args.root.resolve()
    if args.family_only:
        other = "rpm" if args.family_only == "deb" else "deb"
        try:
            paths = optional_files(root, args.family_only).keys() - optional_files(root, other).keys()
        except (OSError, ValueError) as exc:
            print(f"error: {exc}", file=sys.stderr)
            return 1
        for path in sorted(paths):
            print(path)
        return 0
    if args.target is None or args.manifest is None:
        parser.error("--target and --manifest are required to emit the Tauri fragment")
    tauri_dir = root / "lycaon-den" / "src-tauri"
    try:
        floors = floor_values(root / "lycaon" / "internal" / "platformfloor" / "linux_floor.txt")
        catalog = json.loads((root / "packaging" / "linux" / "dependencies.json").read_text(encoding="utf-8"))
        if catalog.get("format_version") != 1:
            raise ValueError("packaging/linux/dependencies.json has an unsupported format")
        required = required_files(root, tauri_dir, args.target, args.manifest.resolve())
        files = {**required, **optional_files(root, "deb")}
        linux = {
            "deb": {
                **render_dependencies(catalog, "deb", floors),
                "files": {destination: str(source) for destination, source in sorted(files.items())},
            }
        }
    except (OSError, ValueError, KeyError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    json.dump({"bundle": {"linux": linux}}, sys.stdout, indent=2, sort_keys=True)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

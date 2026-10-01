#!/usr/bin/env python3
"""Print canonical release metadata as JSON or shell assignments."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import shlex
import sys

from release_semver import parse, parse_release_build, windows_package_version
from update_keys import load_registry, release_binding


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parent.parent)
    parser.add_argument("--format", choices=("json", "shell"), default="json")
    args = parser.parse_args()
    try:
        product = (args.root / "VERSION").read_text(encoding="utf-8").strip()
        if product.startswith("v"):
            raise ValueError("VERSION must not include a leading v")
        build_raw = (args.root / "RELEASE_BUILD").read_text(encoding="utf-8").strip()
        version = parse(product)
        release_build = parse_release_build(build_raw)
        binding = release_binding(load_registry(args.root / "packaging/update-keys.json"), product)
        value = {
            **binding,
            "product_version": product,
            "channel": version.channel,
            "native_version": version.core,
            "release_build": release_build,
            "macos_bundle_version": str(release_build),
            "windows_package_version": windows_package_version(version, release_build),
            "github_prerelease": version.channel == "preview",
            "homebrew_token": (
                "painted-wolf-code@preview"
                if version.channel == "preview"
                else "painted-wolf-code"
            ),
        }
    except (OSError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    if args.format == "json":
        json.dump(value, sys.stdout, sort_keys=True)
        sys.stdout.write("\n")
    else:
        for key, item in value.items():
            rendered = "true" if item is True else "false" if item is False else str(item)
            print(f"{key.upper()}={shlex.quote(rendered)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

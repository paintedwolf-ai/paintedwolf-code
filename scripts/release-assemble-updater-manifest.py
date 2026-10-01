#!/usr/bin/env python3
"""Verify public release builds and assemble the updater manifest."""

from __future__ import annotations

import argparse
import datetime as dt
import json
from pathlib import Path
import re
import sys

from release_semver import parse
from update_keys import load_registry, release_binding


def fail(message: str) -> None:
    raise ValueError(message)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--catalog", type=Path, required=True)
    parser.add_argument("--fragments", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--notes", type=Path, required=True)
    parser.add_argument("--pub-date", required=True)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        version = args.version
        parse(version)
        if args.base_url != args.base_url.rstrip("/") or not args.base_url.startswith("https://"):
            fail("base URL must be an HTTPS origin without a trailing slash")
        published = dt.datetime.fromisoformat(args.pub_date.replace("Z", "+00:00"))
        if published.tzinfo is None:
            fail("publication date requires a timezone")
        catalog = json.loads(args.catalog.read_text(encoding="utf-8"))
        rows = catalog.get("platforms")
        if catalog.get("schema_version") != 1 or not isinstance(rows, list) or not rows:
            fail("release platform catalog has an unsupported shape")
        expected = {row["updater_key"]: row for row in rows}
        if len(expected) != len(rows):
            fail("release platform catalog repeats an updater key")
        for key, row in expected.items():
            if not isinstance(key, str) or not re.fullmatch(r"[a-z0-9]+-[a-z0-9_]+", key):
                fail("release platform catalog has an invalid updater key")
            extension = row.get("updater_extension")
            if not isinstance(extension, str) or not re.fullmatch(r"[A-Za-z0-9.]+", extension):
                fail(f"release platform {key} has an invalid updater extension")
            if row.get("publication") not in {"public", "candidate"}:
                fail(f"release platform {key} has an invalid publication state")
        binding = release_binding(load_registry(), version)
        platforms: dict[str, dict[str, str]] = {}
        landed: set[str] = set()
        for path in sorted(args.fragments.glob("*.json")):
            fragment = json.loads(path.read_text(encoding="utf-8"))
            if set(fragment) != {"platform", "signature", "updater_artifact", "update_keys"}:
                fail(f"{path.name} has an unsupported fragment shape")
            if fragment["update_keys"] != binding:
                fail(f"{path.name} signing and embedded key generations disagree")
            key = fragment["platform"]
            if key not in expected:
                fail(f"{path.name} names unsupported platform {key!r}")
            if key in landed:
                fail(f"duplicate release fragment for {key}")
            if not isinstance(fragment["signature"], str) or not fragment["signature"].strip():
                fail(f"{path.name} has an empty signature")
            updater_extension = expected[key]["updater_extension"]
            wanted_artifact = f"painted-wolf-code_v{version}_{key}.{updater_extension}"
            if fragment["updater_artifact"] != wanted_artifact:
                fail(f"{path.name} updater artifact does not equal {wanted_artifact}")
            landed.add(key)
            if expected[key]["publication"] == "public":
                platforms[key] = {
                    "signature": fragment["signature"],
                    "url": f"{args.base_url}/releases/v{version}/{wanted_artifact}",
                }
        missing = sorted({key for key, row in expected.items() if row["publication"] == "public"} - landed)
        if missing:
            fail(f"missing release fragments: {', '.join(missing)}")
        if not platforms:
            fail("release platform catalog has no public updater platform")
        notes = args.notes.read_text(encoding="utf-8").strip()
        if not notes:
            fail("release notes are empty")
        manifest = {
            "version": version,
            "update_keys": release_binding(load_registry(), version),
            "notes": notes,
            "pub_date": args.pub_date,
            "platforms": dict(sorted(platforms.items())),
        }
        args.output.write_text(
            json.dumps(manifest, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )
    except (KeyError, OSError, TypeError, ValueError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

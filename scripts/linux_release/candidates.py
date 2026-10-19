"""Select and verify non-publishing native Linux package candidates."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys


def matrix(catalog: dict) -> dict:
    if catalog.get("schema_version") != 2:
        raise ValueError("unsupported release platform catalog version")
    rows = [row for row in catalog["platforms"] if row.get("goos") == "linux"]
    if {row.get("goarch") for row in rows} != {"amd64", "arm64"} or len(rows) != 2:
        raise ValueError("Linux candidates require exactly one native row for each architecture")
    for row in rows:
        verify(catalog, row["updater_key"], row["runner"], row["goarch"])
    return {"include": rows}


def verify(catalog: dict, key: str, runner: str, architecture: str) -> dict:
    if catalog.get("schema_version") != 2:
        raise ValueError("unsupported release platform catalog version")
    matches = [row for row in catalog["platforms"] if row.get("updater_key") == key]
    if len(matches) != 1:
        raise ValueError(f"candidate {key!r} must resolve to exactly one catalog row")
    row = matches[0]
    expected = {"amd64": "x86_64", "arm64": "aarch64"}
    if (architecture not in expected or row.get("goos") != "linux"
            or row.get("goarch") != architecture or row.get("artifact_arch") != expected[architecture]
            or row.get("runner") != runner or not isinstance(runner, str) or not runner
            or row.get("publication") not in ("candidate", "public")
            or row.get("package_extensions") != ["deb", "rpm", "pkg.tar.zst"]
            or row.get("updater_extension") is not None):
        raise ValueError(f"candidate {key!r} disagrees with its native package contract")
    return row


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--catalog", type=Path, default=Path("packaging/release-platforms.json"))
    parser.add_argument("--platform")
    parser.add_argument("--runner-label")
    parser.add_argument("--architecture")
    args = parser.parse_args()
    try:
        catalog = json.loads(args.catalog.read_text(encoding="utf-8"))
        result = (verify(catalog, args.platform, args.runner_label, args.architecture)
                  if args.platform else matrix(catalog))
    except (OSError, ValueError, KeyError, TypeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(result, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

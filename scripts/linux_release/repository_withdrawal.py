"""Withdraw a halted release from every Linux repository channel with a retained replacement generation."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import subprocess
import tempfile
import urllib.request

from scripts.release_semver import parse

from . import pacman_r2, repository_r2
from .r2_objects import current
from .release_repositories import STORAGE_PREFIX, withdrawal_key
from .repository_objects import encoded
from .signing import CATALOG

# Every channel a halt can withdraw, including architectures a later catalog may retire.
# test_halt_withdrawal_covers_every_published_repository_channel keeps this table a superset
# of what publication derives from the catalog.
CHANNELS = [(family, channel, architecture) for channel in ["stable", "preview"]
            for family, architecture in [("deb", None), ("rpm", None), ("pacman", "x86_64"), ("pacman", "aarch64")]]


def state_key(family: str, channel: str, architecture: str | None) -> str:
    return "/".join([STORAGE_PREFIX, "channels", family, channel, *([architecture] if architecture else []), "current.json"])


def prepare(bad: str, last_good: str | None, *, read=current) -> list[dict]:
    """Select, per channel offering the halted version, the retained generation that replaces it.

    An earlier offer named as last good is restored exactly. A first Linux release, or a halt that
    names no replacement, activates the signed empty generation retained with the halted release:
    it offers nothing, so installs and upgrades stop and installed packages stay untouched.
    """
    parse(bad)
    rows = []
    with tempfile.TemporaryDirectory(prefix="paintedwolf-linux-withdrawal-") as temporary:
        for family, channel, architecture in CHANNELS:
            active = read(state_key(family, channel, architecture), Path(temporary))
            if active is None or active["version"] != bad or active.get("packages") == []:
                continue
            previous = active.get("previous")
            earlier = None if previous is None else read(f"{STORAGE_PREFIX}/generations/{previous}/generation.json",
                                                         Path(temporary))
            if previous is not None and (earlier is None or hashlib.sha256(encoded(earlier)).hexdigest() != previous):
                raise ValueError(f"Linux {family} {channel} predecessor is not retained")
            if earlier is not None and earlier["packages"] and last_good is not None:
                if earlier.get("version") != last_good:
                    raise ValueError(f"Linux {family} {channel} predecessor is not the retained {last_good} generation")
                receipt = earlier
            else:
                shape = {"family": family, "channel": channel, "version": bad,
                         **({"architecture": architecture} if architecture else {})}
                receipt = read(withdrawal_key(shape), Path(temporary))
                if (receipt is None or receipt.get("packages") != []
                        or any(receipt.get(key) != value for key, value in shape.items())
                        or receipt.get("source_commit") != active.get("source_commit")):
                    raise ValueError(f"Linux {family} {channel} has no retained withdrawal generation for {bad}")
            rows.append({"family": family, "channel": channel, "architecture": architecture, "bad": bad,
                         "active": active["generation"], "replacement": hashlib.sha256(encoded(receipt)).hexdigest(),
                         "receipt": receipt})
    return rows


def retained_path(directory: Path, item: dict) -> Path:
    """A retained object lands inside the reconstruction directory, beside the generation record."""
    path = PurePosixPath(item["path"])
    if (not path.parts or path.is_absolute() or ".." in path.parts or path.as_posix() != item["path"]
            or path.parts == ("generation.json",)):
        raise ValueError(f"retained Linux generation lists an unsupported object path {item['path']!r}")
    return directory / path


def download(base_url: str, row: dict, directory: Path):
    """Reconstruct the retained generation from its public immutable objects."""
    targets = [(item, retained_path(directory, item)) for item in row["receipt"]["objects"]]
    for item, target in targets:
        target.parent.mkdir(parents=True, exist_ok=True)
        url = f"{base_url}/{STORAGE_PREFIX}/generations/{row['replacement']}/{item['path']}"
        with urllib.request.urlopen(urllib.request.Request(url, headers={"User-Agent": "painted-wolf-release/1"}),
                                    timeout=120) as response:
            raw = response.read(item["size"] + 1)
        if len(raw) != item["size"] or hashlib.sha256(raw).hexdigest() != item["sha256"]:
            raise ValueError("retained Linux generation object differs from its inventory")
        target.write_bytes(raw)
    (directory / "generation.json").write_bytes(encoded(row["receipt"]))


def apply(rows: list[dict], base_url: str, *, fetch=download, publishers=None) -> list[dict]:
    publishers = publishers or {"deb": repository_r2.publish_r2, "rpm": repository_r2.publish_r2,
                                "pacman": pacman_r2.publish_r2}
    results, errors = [], []
    for row in rows:
        def authorize(request, row=row):
            if (request["operation"] != "withdraw" or request["expected"] != row["active"]
                    or request["manifest"] != row["receipt"] or request["catalog"] != CATALOG
                    or request["storage_prefix"] != STORAGE_PREFIX):
                raise ValueError("Linux withdrawal differs from its prepared plan")
            return True
        try:
            with tempfile.TemporaryDirectory(prefix="paintedwolf-linux-retained-") as temporary:
                directory = Path(temporary)
                fetch(base_url, row, directory)
                results.append(publishers[row["family"]](row["receipt"], directory, CATALOG, STORAGE_PREFIX, base_url,
                                                         row["active"], False, "withdraw", "activate", authorize=authorize))
        except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
            errors.append(f"{row['family']} {row['channel']}: {error}")
    if errors:
        raise ValueError("Linux withdrawal needs a retry: " + "; ".join(errors))
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=["prepare", "apply"])
    parser.add_argument("--file", type=Path, required=True)
    parser.add_argument("--bad")
    parser.add_argument("--last-good")
    parser.add_argument("--base-url", required=True)
    args = parser.parse_args()
    try:
        if args.operation == "prepare":
            if not args.bad:
                parser.error("prepare requires --bad")
            args.file.write_text(json.dumps(prepare(args.bad, args.last_good or None), indent=2) + "\n")
        else:
            print(json.dumps(apply(json.loads(args.file.read_text()), args.base_url.rstrip("/")), indent=2))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Linux repository withdrawal failed: {exc}\n")


if __name__ == "__main__":
    main()

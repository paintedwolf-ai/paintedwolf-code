"""Withdraw a halted release from every Linux repository channel with a retained replacement generation."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
from pathlib import Path, PurePosixPath
import subprocess
import tempfile
import urllib.request

from scripts.release_semver import parse

from . import pacman_r2, repository_r2, pacman_validation, repository_validation
from .r2_objects import current
from .release_repositories import STORAGE_PREFIX, withdrawal_key, publication_configuration
from release_distribution_contract.platforms import ORIGIN
from .repository_objects import encoded
from .signing import CATALOG

MAX_GENERATIONS = 4096
MAX_MANIFEST_BYTES = 256 << 20

# Retired architectures remain withdrawable after their catalog rows stop publishing.
CHANNELS = [(family, channel, architecture) for channel in ["stable", "preview"]
            for family, architecture in [("deb", None), ("rpm", None), ("pacman", "x86_64"), ("pacman", "aarch64")]]


def state_key(family: str, channel: str, architecture: str | None, *, storage_prefix=STORAGE_PREFIX) -> str:
    return "/".join([storage_prefix, "channels", family, channel, *([architecture] if architecture else []), "current.json"])


class MissingRetainedGeneration(ValueError):
    def __init__(self, family, channel, architecture, selected, versions, *, code="NATIVE_REPLACEMENT_UNAVAILABLE"):
        self.reason = {"code": code, "family": family, "channel": channel, "architecture": architecture,
                       "selected_version": selected, "retained_versions": sorted(versions)}
        super().__init__(json.dumps(self.reason, sort_keys=True))


def retained_generations(read, directory, *, enumerate_prefixes=None, storage_prefix=STORAGE_PREFIX):
    from scripts.release_storage.listing import prefixes
    enumerate_prefixes = enumerate_prefixes or prefixes
    records, size = [], 0
    for number, prefix in enumerate(enumerate_prefixes(storage_prefix + "/generations/"), 1):
        selected = prefix.removeprefix(storage_prefix + "/generations/").removesuffix("/")
        if number > MAX_GENERATIONS or not re.fullmatch("[a-f0-9]{64}", selected):
            raise ValueError("retained generation enumeration exceeds its count or identity bound")
        receipt = read(prefix + "generation.json", directory)
        if not isinstance(receipt, dict):
            raise ValueError("retained generation manifest is unavailable")
        raw = encoded(receipt)
        size += len(raw)
        if size > MAX_MANIFEST_BYTES or hashlib.sha256(raw).hexdigest() != selected:
            raise ValueError("retained generation inventory exceeds its byte bound or differs from its identity")
        if (type(receipt.get("format_version")) is not int or receipt.get("format_version") != 1 or receipt.get("family") not in {"deb", "rpm", "pacman"}
                or receipt.get("channel") not in {"stable", "preview"} or type(receipt.get("rehearsal")) is not bool
                or not isinstance(receipt.get("version"), str) or not isinstance(receipt.get("packages"), list)):
            raise ValueError("retained generation has an unsupported publication shape")
        parse(receipt["version"])
        records.append(receipt)
    return records


def prepare(distribution: dict, *, read=current, enumerate_prefixes=None, rehearsal=False, catalog=None,
            storage_prefix=STORAGE_PREFIX, base_url=ORIGIN, transport=None) -> list[dict]:
    """Resolve only the common plan's per-channel replacements from authenticated retained manifests."""
    from release_distribution_contract.halt_plan import validate
    validate(distribution)
    catalog = catalog or CATALOG
    publication_configuration(catalog, storage_prefix, base_url, rehearsal)
    if rehearsal and transport is None:
        raise ValueError("rehearsal withdrawal requires its explicit isolated transport")
    if transport is not None:
        read, enumerate_prefixes = transport.current, transport.prefixes
    rows, targets, kept = [], [], []
    with tempfile.TemporaryDirectory(prefix="paintedwolf-linux-withdrawal-") as temporary:
        directory = Path(temporary)
        for selected in distribution["channels"]:
            for family, channel, architecture in CHANNELS:
                if channel != selected["channel"]:
                    continue
                active = read(state_key(family, channel, architecture, storage_prefix=storage_prefix), directory)
                if active is not None and active["version"] == selected["bad"] and active.get("packages") != []:
                    targets.append((selected, family, channel, architecture, active))
                else:
                    kept.append({"action": "keep", "storage_prefix": storage_prefix, "rehearsal": rehearsal,
                                 "family": family, "channel": channel, "architecture": architecture, "bad": selected["bad"],
                                 "active": active["generation"] if active else None,
                                 "advertised_version": active["version"] if active else None})
        retained = retained_generations(read, directory, enumerate_prefixes=enumerate_prefixes, storage_prefix=storage_prefix) if any(
            selected["last_good"] is not None and not selected["last_good_legacy"] for selected, *_ in targets) else []
        for selected, family, channel, architecture, active in targets:
            bad, good = selected["bad"], selected["last_good"]
            if good is not None and not selected["last_good_legacy"]:
                available = [receipt for receipt in retained if receipt["family"] == family
                             and receipt["channel"] == channel and receipt.get("architecture") == architecture
                             and receipt["rehearsal"] is rehearsal and receipt["packages"]]
                matches = [receipt for receipt in available if receipt["version"] == good]
                if len(matches) != 1:
                    raise MissingRetainedGeneration(family, channel, architecture, good,
                        {receipt["version"] for receipt in available},
                        code="NATIVE_REPLACEMENT_UNAVAILABLE" if not matches else "NATIVE_REPLACEMENT_AMBIGUOUS")
                receipt = matches[0]
            else:
                shape = {"family": family, "channel": channel, "version": bad,
                         **({"architecture": architecture} if architecture else {})}
                receipt = read(withdrawal_key(shape, storage_prefix=storage_prefix), directory)
                if (receipt is None or receipt.get("packages") != []
                        or any(receipt.get(key) != value for key, value in shape.items())
                        or receipt.get("source_commit") != active.get("source_commit")):
                    raise ValueError(f"Linux {family} {channel} has no retained withdrawal generation for {bad}")
            validator = pacman_validation if family == "pacman" else repository_validation
            validator.validate_manifest(receipt, catalog, rehearsal)
            rows.append({"action": "withdraw", "storage_prefix": storage_prefix, "rehearsal": rehearsal, "family": family, "channel": channel, "architecture": architecture, "bad": bad,
                         "active": active["generation"], "replacement": hashlib.sha256(encoded(receipt)).hexdigest(),
                         "receipt": receipt})
    return rows + kept


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
        url = f"{base_url}/{row.get('storage_prefix', STORAGE_PREFIX)}/generations/{row['replacement']}/{item['path']}"
        with urllib.request.urlopen(urllib.request.Request(url, headers={"User-Agent": "painted-wolf-release/1"}),
                                    timeout=120) as response:
            raw = response.read(item["size"] + 1)
        if len(raw) != item["size"] or hashlib.sha256(raw).hexdigest() != item["sha256"]:
            raise ValueError("retained Linux generation object differs from its inventory")
        target.write_bytes(raw)
    (directory / "generation.json").write_bytes(encoded(row["receipt"]))


def apply(rows: list[dict], base_url: str, *, fetch=download, publishers=None, rehearsal=False, catalog=None,
          storage_prefix=STORAGE_PREFIX, transport=None) -> list[dict]:
    catalog = catalog or CATALOG
    publication_configuration(catalog, storage_prefix, base_url, rehearsal)
    if rehearsal and transport is None:
        raise ValueError("rehearsal withdrawal requires its explicit isolated transport")
    if any(row.get("storage_prefix") != storage_prefix or row.get("rehearsal") is not rehearsal
           or row.get("action") not in {"withdraw", "keep"} for row in rows):
        raise ValueError("withdrawal target differs from the prepared trust mode or namespace")
    publishers = publishers or {"deb": repository_r2.publish_r2, "rpm": repository_r2.publish_r2,
                                "pacman": pacman_r2.publish_r2}
    results, errors = [], []
    for row in rows:
        if row["action"] == "keep":
            continue
        def authorize(request, row=row):
            if (request["operation"] != "withdraw" or request["expected"] != row["active"]
                    or request["manifest"] != row["receipt"] or request["catalog"] != catalog
                    or request["storage_prefix"] != storage_prefix):
                raise ValueError("Linux withdrawal differs from its prepared plan")
            return True
        try:
            with tempfile.TemporaryDirectory(prefix="paintedwolf-linux-retained-") as temporary:
                directory = Path(temporary)
                fetch(base_url, row, directory)
                results.append(publishers[row["family"]](row["receipt"], directory, catalog, storage_prefix, base_url,
                                                         row["active"], rehearsal, "withdraw", "activate", authorize=authorize,
                                                         **({"transport": transport} if transport is not None else {})))
        except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
            errors.append(f"{row['family']} {row['channel']}: {error}")
    if errors:
        raise ValueError("Linux withdrawal needs a retry: " + "; ".join(errors))
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=["prepare", "apply"])
    parser.add_argument("--file", type=Path, required=True)
    parser.add_argument("--distribution-plan", type=Path)
    parser.add_argument("--base-url", required=True)
    args = parser.parse_args()
    try:
        if args.operation == "prepare":
            if args.distribution_plan is None:
                parser.error("prepare requires --distribution-plan")
            args.file.write_text(json.dumps(prepare(json.loads(args.distribution_plan.read_bytes()), base_url=args.base_url.rstrip("/")), indent=2) + "\n")
        else:
            print(json.dumps(apply(json.loads(args.file.read_text()), args.base_url.rstrip("/")), indent=2))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Linux repository withdrawal failed: {exc}\n")


if __name__ == "__main__":
    main()

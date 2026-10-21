"""Withdraw a halted version from every enabled apt, dnf and pacman repository channel.

Clients cache the last index they fetched, so deleting a channel's authority does not stop
them offering the halted version. Withdrawal republishes the channel's retained healthy
predecessor through the repository owners' own `withdraw` operation, which also records
tombstones so neither generation can be offered again.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import urllib.request

from ..trust.authority import PREFIX, canonical, repository_origin
from ..trust.generations import CATALOG
from ...repository_operations import OPERATIONS, identity, version_marker

PRODUCTION_ORIGIN = "https://downloads.paintedwolf.dev"
CHANNEL_TARGETS = [("deb", None), ("rpm", None), ("pacman", "x86_64"), ("pacman", "aarch64")]
HEX = re.compile(r"[0-9a-f]{64}")
MAX_OBJECT = 1 << 30


def channel_path(family, channel, architecture):
    return f"channels/{family}/{channel}" + ("/" + architecture if architecture else "")


def admit(manifest, directory, catalog, rehearsal):
    if manifest["family"] == "pacman":
        from ...pacman_validation import admit as family_admit
    else:
        from ...repository_validation import admit as family_admit
    family_admit(manifest, Path(directory), Path(catalog), rehearsal)


def withdrawal_of(active, replacement_generation):
    """The record the owner's withdraw operation leaves once the replacement is active."""
    return lambda current: (isinstance(current, dict) and current.get("operation") == "withdraw"
                            and current.get("generation") == replacement_generation
                            and current.get("previous") == active["generation"])


def active_record(current, family, channel, architecture, rehearsal):
    if current is None:
        return None
    if (not isinstance(current, dict) or current.get("format_version") != 1
            or current.get("operation") not in OPERATIONS
            or not isinstance(current.get("generation"), str) or not HEX.fullmatch(current["generation"])
            or current.get("family") != family or current.get("channel") != channel
            or current.get("architecture") != architecture or current.get("rehearsal") is not rehearsal
            or not isinstance(current.get("version"), str)):
        raise ValueError(f"{channel_path(family, channel, architecture)} has an unsupported active state")
    return current


def fetch_generation(manifest, generation, directory, fetch):
    for row in manifest["objects"]:
        path = PurePosixPath(row["path"])
        if path.is_absolute() or ".." in path.parts or path.as_posix() != row["path"]:
            raise ValueError("retained generation lists an escaping object")
        target = directory / row["path"]
        target.parent.mkdir(parents=True, exist_ok=True)
        fetch(f"generations/{generation}/{row['path']}", target, {"sha256": row["sha256"], "bytes": row["size"]})


def replacement(row, active, family, architecture, *, read, fetch, work, catalog, rehearsal):
    where = channel_path(family, row["channel"], architecture)
    if row["last_good"] is None:
        raise ValueError(f"{where} offers {row['bad']} and indexes are cached by clients; "
                         "name a last-good release whose generation this channel retains")
    previous = active.get("previous")
    if not isinstance(previous, str) or not HEX.fullmatch(previous):
        raise ValueError(f"{where} has no retained predecessor to restore")
    manifest = read(f"generations/{previous}/generation.json")
    if not isinstance(manifest, dict) or identity(manifest) != previous:
        raise ValueError(f"{where} predecessor differs from its retained immutable generation")
    if (manifest.get("family"), manifest.get("channel"), manifest.get("architecture")) != (family, row["channel"], architecture):
        raise ValueError(f"{where} predecessor belongs to another channel")
    if manifest.get("version") != row["last_good"] or manifest["version"] == row["bad"]:
        raise ValueError(f"{where} retains {manifest.get('version')} before {row['bad']}, "
                         f"not the selected last-good {row['last_good']}")
    signer = ("signing_generation", "key_fingerprint")
    if any(manifest.get(name) != active.get(name) for name in signer):
        raise ValueError(f"{where} predecessor was signed by another key generation; recover it re-signed first")
    if read(f"withdrawn/{previous}.json") is not None or read(version_marker(manifest).as_posix()) is not None:
        raise ValueError(f"{where} predecessor was itself withdrawn")
    directory = work / where / previous
    fetch_generation(manifest, previous, directory, fetch)
    admit(manifest, directory, catalog, rehearsal)
    return manifest, directory


def prepare(distribution, *, read, fetch, work, catalog=CATALOG, storage_prefix=PREFIX,
            origin=PRODUCTION_ORIGIN, rehearsal=False):
    """Read and freshly admit every channel without changing any repository object."""
    entries = []
    for row in distribution["channels"]:
        for family, architecture in CHANNEL_TARGETS:
            where = channel_path(family, row["channel"], architecture)
            active = active_record(read(where + "/current.json"), family, row["channel"], architecture, rehearsal)
            entry = {"family": family, "channel": row["channel"], "architecture": architecture,
                     "bad_version": row["bad"], "active": active, "replacement": None, "directory": None}
            if active is None:
                entry["state"] = "absent"
            elif active["operation"] == "withdraw" and active.get("withdrawn_version") == row["bad"]:
                entry["state"] = "withdrawn"
            elif active["version"] != row["bad"]:
                entry["state"] = "unaffected"
            else:
                manifest, directory = replacement(row, active, family, architecture, read=read, fetch=fetch,
                                                  work=work, catalog=catalog, rehearsal=rehearsal)
                entry.update(state="replace", replacement=manifest, directory=str(directory))
            entries.append(entry)
    return {"format_version": 1, "storage_prefix": storage_prefix, "origin": origin,
            "catalog": str(Path(catalog).resolve()), "rehearsal": rehearsal, "channels": entries}


class WithdrawalAuthority:
    """Admit only the prepared replacement for the prepared active generation."""

    def __init__(self, plan, entry, read):
        self.plan, self.entry, self.read = plan, entry, read
        self.restored = withdrawal_of(entry["active"], identity(entry["replacement"]))

    def __call__(self, request):
        required = {"manifest", "directory", "catalog", "storage_prefix", "base_url",
                    "expected", "operation", "phase"}
        entry = self.entry
        if not isinstance(request, dict) or set(request) != required:
            raise ValueError("native halt requires its complete fixed transport request")
        if (request["operation"] != "withdraw" or request["phase"] != "activate"
                or request["storage_prefix"] != self.plan["storage_prefix"]
                or request["base_url"] != self.plan["origin"]
                or str(Path(request["catalog"]).resolve()) != self.plan["catalog"]):
            raise ValueError("native halt differs from protected withdrawal policy")
        if (canonical(request["manifest"]) != canonical(entry["replacement"])
                or request["manifest"]["version"] == entry["bad_version"]
                or request["expected"] != entry["active"]["generation"]
                or Path(request["directory"]).resolve() != Path(entry["directory"]).resolve()):
            raise ValueError("native halt differs from its prepared replacement")
        current = self.read(channel_path(entry["family"], entry["channel"], entry["architecture"]) + "/current.json")
        if current != entry["active"] and not self.restored(current):
            raise ValueError("repository channel changed since the halt was prepared")
        admit(request["manifest"], request["directory"], self.plan["catalog"], self.plan["rehearsal"])
        return True


def transport(family):
    if family == "pacman":
        from ...pacman_r2 import publish_r2
    else:
        from ...repository_r2 import publish_r2
    return publish_r2


def apply(plan, *, read):
    """Republish each prepared replacement; an interrupted halt resumes with the same plan."""
    errors, results = [], []
    for entry in plan["channels"]:
        where = channel_path(entry["family"], entry["channel"], entry["architecture"])
        try:
            current = read(where + "/current.json")
            if entry["state"] != "replace":
                if current != entry["active"]:
                    raise ValueError(f"{where} changed since the halt was prepared")
                results.append({"channel": where, "state": entry["state"]})
                continue
            authority = WithdrawalAuthority(plan, entry, read)
            if current != entry["active"] and not authority.restored(current):
                raise ValueError(f"{where} changed since the halt was prepared")
            record = transport(entry["family"])(
                entry["replacement"], Path(entry["directory"]), Path(plan["catalog"]), plan["storage_prefix"],
                plan["origin"], entry["active"]["generation"], plan["rehearsal"], "withdraw", "activate",
                authorize=authority)
            results.append({"channel": where, "state": "withdrawn", "generation": record["generation"],
                            "withdrawn_version": record["withdrawn_version"]})
        except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
            errors.append(f"{where}: {error}")
    if errors:
        raise ValueError("native repository withdrawal needs a retry: " + "; ".join(errors))
    return results


def verified_copy(stream, target, name, expected):
    """Bytes are accepted only when they equal the generation's sealed inventory row."""
    digest, size = hashlib.sha256(), 0
    with target.open("xb") as output:
        while chunk := stream.read(1 << 20):
            size += len(chunk)
            if size > min(expected["bytes"], MAX_OBJECT):
                raise ValueError(f"retained repository object {name} exceeds its recorded size")
            digest.update(chunk)
            output.write(chunk)
    if size != expected["bytes"] or digest.hexdigest() != expected["sha256"]:
        raise ValueError(f"retained repository object {name} differs from its generation inventory")


def public_fetch(base):
    def fetch(name, target, expected):
        request = urllib.request.Request(base + "/" + name, headers={"User-Agent": "painted-wolf-release/1"})
        with urllib.request.urlopen(request, timeout=60) as response:
            verified_copy(response, target, name, expected)
    return fetch


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    prepared = commands.add_parser("prepare")
    prepared.add_argument("--distribution", type=Path, required=True)
    prepared.add_argument("--work", type=Path, required=True)
    prepared.add_argument("--output", type=Path, required=True)
    applied = commands.add_parser("apply")
    applied.add_argument("--plan", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        configured = os.environ.get("LINUX_REPOSITORY_ORIGIN", "")
        plan = json.loads(args.plan.read_text()) if args.command == "apply" else None
        if not configured:
            if plan is not None and plan != {"format_version": 1, "configured": False}:
                raise ValueError("native halt was prepared for a configured repository origin")
            print("Native repositories are not configured for publication; no channel can offer the halted version.")
            if args.command == "prepare":
                with args.output.open("x") as stream:
                    stream.write(json.dumps({"format_version": 1, "configured": False}) + "\n")
            return 0
        origin = repository_origin(configured)
        if origin != PRODUCTION_ORIGIN:
            raise ValueError("native halt requires the protected production repository origin")
        from .repository_observation import ReadOnlyOrigin
        backend = ReadOnlyOrigin(origin)
        if args.command == "prepare":
            distribution = json.loads(args.distribution.read_text())
            work = args.work.absolute()
            work.mkdir(mode=0o700)
            plan = prepare(distribution, read=backend.read, fetch=public_fetch(backend.public), work=work,
                           origin=origin)
            with args.output.open("x") as stream:
                json.dump(plan, stream, indent=2)
                stream.write("\n")
            for entry in plan["channels"]:
                print(f"{channel_path(entry['family'], entry['channel'], entry['architecture'])}: {entry['state']}")
            return 0
        if plan.get("configured") is False:
            raise ValueError("native halt was prepared without a configured repository origin")
        if (plan.get("origin") != origin or plan.get("storage_prefix") != PREFIX
                or plan.get("catalog") != str(CATALOG.resolve()) or plan.get("rehearsal") is not False):
            raise ValueError("native halt plan differs from protected production policy")
        for result in apply(plan, read=backend.read):
            print(json.dumps(result, sort_keys=True))
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        parser.exit(1, f"Native repository withdrawal refused: {error}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

"""Publication timestamps and independently verifiable distribution receipts."""
from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import re
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import urllib.parse

from release_semver import compare, parse
from release_distribution_contract import ledger

# Cloudflare refuses Python's default urllib agent on the public release domains.
PUBLIC_READ_HEADERS = {"Cache-Control": "no-cache, no-store", "User-Agent": "painted-wolf-release/1"}

ROOT = Path(__file__).resolve().parent.parent


def encoded(value: dict) -> bytes:
    return (json.dumps(value, sort_keys=True, indent=2) + "\n").encode()


def timestamp(value: str) -> dt.datetime:
    parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise ValueError("release timestamp requires a timezone")
    return parsed


def activation_manifest(manifest: dict, record: dict) -> dict:
    expected = hashlib.sha256(encoded(manifest)).hexdigest()
    if set(record) != {"version", "manifest_sha256", "started_at"} or record["version"] != manifest["version"] or record["manifest_sha256"] != expected:
        raise ValueError("activation record differs from the immutable release")
    timestamp(record["started_at"])
    if manifest.get("withdrawn", False):
        raise ValueError("a withdrawn manifest cannot be activated")
    return {**manifest, "pub_date": record["started_at"]}


def storage_bytes(url: str, token: str) -> bytes:
    request = urllib.request.Request(url, headers={"Authorization": "Bearer " + token, "Cache-Control": "no-cache"})
    with urllib.request.urlopen(request, timeout=30) as response:
        raw = response.read(2 * 1024 * 1024 + 1)
        if len(raw) > 2 * 1024 * 1024:
            raise ValueError("release storage metadata exceeds its bound")
        return raw


def storage_json(url: str, token: str) -> tuple[bytes, dict]:
    raw = storage_bytes(url, token)
    return raw, json.loads(raw)


def read_storage_bytes(key: str) -> bytes | None:
    if os.environ.get("RELEASE_R2_TRANSPORT") == "s3":
        from release_storage.s3 import read_bytes
        return read_bytes(key)
    account, bucket, token = (os.environ[name] for name in ("CLOUDFLARE_ACCOUNT_ID", "R2_BUCKET", "CLOUDFLARE_API_TOKEN"))
    base = f"https://api.cloudflare.com/client/v4/accounts/{account}/r2/buckets/{bucket}/objects"
    _, listing = storage_json(base + "?" + urllib.parse.urlencode({"prefix": key, "per_page": 20}), token)
    if listing.get("success") is not True:
        raise ValueError("release storage listing failed")
    objects = listing.get("result")
    if isinstance(objects, dict):
        objects = objects.get("objects", objects.get("keys"))
    if not isinstance(objects, list):
        raise ValueError("release storage listing has an invalid shape")
    found = [obj for obj in objects if obj.get("key") == key]
    if not found:
        return None
    etag = found[0]["etag"].strip('"')
    # The listing establishes existence while GET may still return a cached miss.
    for attempt in range(12):
        try:
            raw = storage_bytes(base + "/" + urllib.parse.quote(key, safe="/"), token)
            if hashlib.md5(raw).hexdigest() == etag:
                return raw
        except urllib.error.HTTPError as error:
            if error.code != 404:
                raise
        if attempt < 11:
            time.sleep(5)
    raise ValueError("release storage bytes did not converge to the listed object: " + key)


def decode_storage(raw: bytes | None) -> dict | None:
    if raw is None:
        return None
    value = json.loads(raw)
    if not isinstance(value, dict):
        raise ValueError("present release storage control must be a JSON object")
    return value


def read_storage(key: str) -> dict | None:
    return decode_storage(read_storage_bytes(key))


def validate_advance(manifest: dict, current: dict | None, optional: bool = False) -> bool:
    if manifest.get("withdrawn", False):
        raise ValueError("a withdrawn manifest cannot be activated")
    if current is None:
        return True
    order = compare(parse(manifest["version"]), parse(current["version"]))
    if order > 0:
        return True
    if order == 0 and {k: v for k, v in manifest.items() if k != "pub_date"} == {k: v for k, v in current.items() if k != "pub_date"}:
        return True
    if order < 0 and optional:
        return False
    raise ValueError("updater pointer may only advance or retry the identical release")


def cask_version(content: bytes) -> str:
    versions = re.findall(rb'^  version "([^"\n]+)"$', content, re.MULTILINE)
    if len(versions) != 1:
        raise ValueError("package must declare exactly one release version")
    version = versions[0].decode("ascii")
    parse(version)
    return version


def stage_casks(source: Path, tap: Path, channel: str, *, channels: list[str] | None = None) -> None:
    expected = {"painted-wolf-code@preview.rb"}
    if channel == "stable":
        expected.add("painted-wolf-code.rb")
    candidates = sorted(source.glob("*.rb"))
    if {candidate.name for candidate in candidates} != expected:
        raise ValueError("release package set differs from its channel")
    changes = []
    for candidate in candidates:
        if channels is not None and candidate.name == "painted-wolf-code@preview.rb" and "preview" not in channels:
            continue
        incoming = candidate.read_bytes()
        version = cask_version(incoming)
        destination = tap / "Casks" / candidate.name
        if destination.exists():
            current = destination.read_bytes()
            order = compare(parse(version), parse(cask_version(current)))
            if order == 0:
                if current != incoming:
                    raise ValueError("published package changed for the same version")
                continue
            if order < 0:
                if channel == "stable" and candidate.name == "painted-wolf-code@preview.rb":
                    continue
                raise ValueError("package publication would regress its channel")
        changes.append((destination, incoming))
    for destination, content in changes:
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(content)


def activate(path: Path, output: Path, storage_prefix: str = "") -> None:
    manifest = json.loads(path.read_text())
    ledger.require_publishable(manifest["version"])
    if manifest.get("withdrawn", False):
        raise ValueError("a withdrawn manifest cannot be activated")
    key = f"release-metadata/{manifest['version']}/activation.json"
    if storage_prefix:
        key = storage_prefix + "/" + key
    record = read_storage(key)
    if record is None:
        record = {"version": manifest["version"], "manifest_sha256": hashlib.sha256(encoded(manifest)).hexdigest(),
                  "started_at": dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds")}
        with tempfile.TemporaryDirectory(prefix="release-activation-") as directory:
            source = Path(directory) / "activation.json"
            source.write_bytes(encoded(record))
            subprocess.run(["bash", str(ROOT / "scripts/release-r2-immutable-put.sh"), "--key", key, "--file", str(source),
                            "--content-type", "application/json"], check=True)
    output.write_bytes(encoded(activation_manifest(manifest, record)))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    read = commands.add_parser("storage-read")
    read.add_argument("--key", required=True)
    read.add_argument("--output", type=Path, required=True)
    advance = commands.add_parser("advance")
    advance.add_argument("--file", type=Path, required=True)
    advance.add_argument("--key", required=True)
    advance.add_argument("--optional", action="store_true")
    withdraw = commands.add_parser("withdraw")
    withdraw.add_argument("--file", type=Path, required=True)
    withdraw.add_argument("--prepared", type=Path)
    prepare_ledger = commands.add_parser("prepare-withdrawals")
    prepare_ledger.add_argument("--file", type=Path, required=True)
    prepare_ledger.add_argument("--output", type=Path, required=True)
    casks = commands.add_parser("casks")
    casks.add_argument("--source", type=Path, required=True)
    casks.add_argument("--tap", type=Path, required=True)
    casks.add_argument("--channel", choices=("stable", "preview"), required=True)
    casks.add_argument("--plan", type=Path, required=True)
    activation = commands.add_parser("activate")
    activation.add_argument("--file", type=Path, required=True)
    activation.add_argument("--output", type=Path, required=True)
    activation.add_argument("--storage-prefix", default="")
    website = commands.add_parser("website")
    website.add_argument("--base-url", default="https://paintedwolf.ai")
    website.add_argument("--channel", choices=("stable", "preview"), required=True)
    website.add_argument("--version")
    website.add_argument("--manifest-sha256")
    website.add_argument("--distribution-sha256")
    website.add_argument("--withdrawn-version")
    website.add_argument("--plan", type=Path, required=True)
    website.add_argument("--generation", type=int, default=1)
    website.add_argument("--catalog", type=Path, default=ROOT / "packaging/release-platforms.json")
    args = parser.parse_args()
    if args.command == "storage-read":
        value = read_storage_bytes(args.key)
        if value is not None:
            args.output.write_bytes(value)
        print(404 if value is None else 200)
    elif args.command == "advance":
        manifest = json.loads(args.file.read_text())
        ledger.require_publishable(manifest["version"])
        print(str(validate_advance(manifest, read_storage(args.key), args.optional)).lower())
    elif args.command == "prepare-withdrawals":
        args.output.write_text(json.dumps(ledger.prepare(json.loads(args.file.read_bytes())["bad_versions"]), indent=2) + "\n")
    elif args.command == "withdraw":
        versions = json.loads(args.file.read_bytes())["bad_versions"]
        rows = json.loads(args.prepared.read_bytes()) if args.prepared is not None else ledger.prepare(versions)
        ledger.record(rows, versions)
    elif args.command == "casks":
        for candidate in args.source.glob("*.rb"):
            ledger.require_publishable(cask_version(candidate.read_bytes()))
        from release_distribution_contract.channels import check
        plan = json.loads(args.plan.read_bytes())
        check(plan)
        if plan["channel"] != args.channel:
            raise ValueError("cask channel differs from the selected publication plan")
        stage_casks(args.source, args.tap, args.channel, channels=plan["channels"])
    elif args.command == "activate":
        activate(args.file, args.output, args.storage_prefix)
    else:
        from release_distribution_contract.confirmation import wait_for_website
        from release_distribution_contract.channels import check
        plan = json.loads(args.plan.read_bytes())
        check(plan)
        if plan["channel"] != args.channel or plan["version"] != args.version or plan["signing_generation"] != args.generation:
            raise ValueError("website differs from the selected publication plan")
        for channel in plan["channels"]:
            wait_for_website(args.base_url, {"channel": channel, "version": args.version,
                             "manifest_sha256": args.manifest_sha256, "distribution_sha256": args.distribution_sha256,
                             "withdrawn_version": args.withdrawn_version},
                             catalog=args.catalog)


if __name__ == "__main__":
    main()

"""Publication timestamps and independently verifiable distribution receipts."""
from __future__ import annotations

import argparse
import datetime as dt
import hashlib
from html.parser import HTMLParser
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
from release_distribution_contract.platforms import package_urls
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


def read_storage(key: str) -> dict | None:
    raw = read_storage_bytes(key)
    return json.loads(raw) if raw is not None else None


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


def validate_website_receipt(receipt: dict, expected: dict) -> None:
    version = receipt.get("schema_version")
    if type(version) is not int or version not in (1, 2) or "distribution_sha256" in expected and version != 2:
        raise ValueError("website release receipt has an unsupported shape")
    if receipt.get("channel") not in {"stable", "preview"}:
        raise ValueError("website receipt requires its typed release channel")
    release = receipt.get("version")
    if release is None:
        if receipt.get("manifest_sha256") is not None or receipt.get("distribution_sha256") is not None:
            raise ValueError("unpublished website receipt cannot retain release identities")
    else:
        parse(release)
        from release_distribution_contract.document_schema import digest
        digest(receipt.get("manifest_sha256"))
        if receipt.get("distribution_sha256") is not None:
            digest(receipt["distribution_sha256"])
    if receipt.get("withdrawn_version") is not None:
        parse(receipt["withdrawn_version"])
    wanted = {"schema_version": version, **expected}
    if version == 2 and "distribution_sha256" not in expected:
        wanted["distribution_sha256"] = None
    if receipt != wanted:
        raise ValueError("website has not published the requested distribution state")


class WebsiteDownloads(HTMLParser):
    def __init__(self):
        super().__init__()
        self.channels = {"stable": [], "preview": []}

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        channel = attrs.get("data-release-channel")
        if tag == "a" and channel in self.channels and "download" in attrs:
            self.channels[channel].append(attrs.get("href"))


def validate_website_downloads(html: str, expected: dict, *, catalog: Path | None = None,
                              origin: str = "https://downloads.paintedwolf.dev") -> None:
    parser = WebsiteDownloads()
    parser.feed(html)
    version = expected["version"]
    urls = [] if version is None else package_urls(version, catalog or ROOT / "packaging/release-platforms.json", origin=origin)
    if sorted(parser.channels[expected["channel"]], key=lambda value: value or "") != urls:
        raise ValueError("website download links do not match its publication confirmation")


def no_store(headers) -> None:
    cache = headers.get("Cache-Control", "").lower()
    if "no-store" not in {part.strip() for part in cache.split(",")}:
        raise ValueError("website release state must be served with Cache-Control: no-store")
    if headers.get("CF-Cache-Status", "").upper() in {"HIT", "STALE", "UPDATING"} or int(headers.get("Age", "0")) > 0:
        raise ValueError("website release state was served from a cache")


def wait_for_website(base: str, expected: dict, seconds: int = 1800, *, allow_newer: bool = False, catalog: Path | None = None, legacy_catalog: bool = False, rehearsal: bool = False, registry=None, origin: str = "https://downloads.paintedwolf.dev", fetch=None) -> None:
    from release_distribution_contract.remote_document import trust
    trust(rehearsal, registry, origin)
    if rehearsal and not callable(fetch):
        raise ValueError("rehearsal confirmation requires its explicit isolated document transport")
    if not base.startswith("https://") or expected["channel"] not in ("stable", "preview"):
        raise ValueError("website verification requires HTTPS and a release channel")
    if type(legacy_catalog) is not bool:
        raise ValueError("legacy publication selection must be explicit")
    if legacy_catalog:
        from release_distribution_contract.platforms import LEGACY_PLATFORMS as historical_catalog
        catalog = historical_catalog
    elif expected["version"] is not None and not expected.get("distribution_sha256"):
        raise ValueError("website confirmation requires the selected distribution digest")
    deadline = time.monotonic() + seconds
    last_error = "no receipt"
    url = f"{base.rstrip('/')}/.well-known/releases/{expected['channel']}.json"
    while True:
        try:
            request = urllib.request.Request(url, headers=PUBLIC_READ_HEADERS)
            with urllib.request.urlopen(request, timeout=20) as response:
                raw = response.read(16385)
                if len(raw) > 16384:
                    raise ValueError("website receipt exceeds its bound")
                receipt = json.loads(raw)
                accepted = expected
                if allow_newer and receipt.get("version") is not None and compare(parse(receipt["version"]), parse(expected["version"])) > 0:
                    if expected["channel"] != "preview" or receipt.get("channel") != "preview" or not re.fullmatch(r"[a-f0-9]{64}", receipt.get("manifest_sha256", "")):
                        raise ValueError("invalid newer Preview receipt")
                    accepted = {key: receipt.get(key) for key in expected}
                validate_website_receipt(receipt, accepted)
                no_store(response.headers)
            if accepted.get("distribution_sha256") is not None:
                from release_distribution_contract.remote_document import confirm_public
                options = {"rehearsal": rehearsal, "registry": registry, "origin": origin}
                if fetch is not None:
                    options["fetch"] = fetch
                confirm_public(accepted, **options)
            elif accepted["version"] is not None and not legacy_catalog:
                raise ValueError("new publication cannot select the legacy package baseline")
            # Preview builds are offered only on the unlisted preview page.
            page = "/download/" if expected["channel"] == "stable" else "/download/preview/"
            request = urllib.request.Request(base.rstrip("/") + page, headers=PUBLIC_READ_HEADERS)
            with urllib.request.urlopen(request, timeout=20) as response:
                no_store(response.headers)
                raw = response.read(2 * 1024 * 1024 + 1)
                if len(raw) > 2 * 1024 * 1024:
                    raise ValueError("website download page exceeds its bound")
                validate_website_downloads(raw.decode(), accepted, catalog=catalog,
                    origin="https://downloads.paintedwolf.dev" if legacy_catalog else origin)
            return
        except (OSError, ValueError) as error:
            last_error = str(error)
        if time.monotonic() >= deadline:
            raise ValueError("website publication was not verified: " + last_error)
        time.sleep(min(5, max(0, deadline - time.monotonic())))


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
        from release_distribution_contract.channels import check
        plan = json.loads(args.plan.read_bytes())
        check(plan)
        if plan["channel"] != args.channel or plan["version"] != args.version or plan["signing_generation"] != args.generation:
            raise ValueError("website differs from the selected publication plan")
        for channel in plan["channels"]:
            wait_for_website(args.base_url, {"channel": channel, "version": args.version,
                             "manifest_sha256": args.manifest_sha256, "distribution_sha256": args.distribution_sha256,
                             "withdrawn_version": args.withdrawn_version},
                             allow_newer=args.channel == "stable" and channel == "preview", catalog=args.catalog)


if __name__ == "__main__":
    main()

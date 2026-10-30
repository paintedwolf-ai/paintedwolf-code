"""Confirm the selected public website receipt, signed document and exact package links."""
from html.parser import HTMLParser
import json
from pathlib import Path
import time
import urllib.request

from release_semver import parse
from release_distribution import PUBLIC_READ_HEADERS
from .platforms import package_urls, ORIGIN

ROOT = Path(__file__).resolve().parents[2]


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
                              origin: str = ORIGIN) -> None:
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


def wait_for_website(base: str, expected: dict, seconds: int = 1800, *, catalog: Path | None = None, legacy_catalog: bool = False, rehearsal: bool = False, registry=None, origin: str = ORIGIN, fetch=None) -> None:
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
                validate_website_receipt(receipt, expected)
                no_store(response.headers)
            if expected.get("distribution_sha256") is not None:
                from release_distribution_contract.remote_document import confirm_public
                options = {"rehearsal": rehearsal, "registry": registry, "origin": origin}
                if fetch is not None:
                    options["fetch"] = fetch
                confirm_public(expected, **options)
            elif expected["version"] is not None and not legacy_catalog:
                raise ValueError("new publication cannot select the legacy package baseline")
            # Preview builds are offered only on the unlisted preview page.
            page = "/download/" if expected["channel"] == "stable" else "/download/preview/"
            request = urllib.request.Request(base.rstrip("/") + page, headers=PUBLIC_READ_HEADERS)
            with urllib.request.urlopen(request, timeout=20) as response:
                no_store(response.headers)
                raw = response.read(2 * 1024 * 1024 + 1)
                if len(raw) > 2 * 1024 * 1024:
                    raise ValueError("website download page exceeds its bound")
                validate_website_downloads(raw.decode(), expected, catalog=catalog,
                    origin=ORIGIN if legacy_catalog else origin)
            return
        except (OSError, ValueError) as error:
            last_error = str(error)
        if time.monotonic() >= deadline:
            raise ValueError("website publication was not verified: " + last_error)
        time.sleep(min(5, max(0, deadline - time.monotonic())))

"""Confirm every website package link from independently pinned signed distribution bytes."""
import hashlib
from pathlib import Path
import re
import tempfile
import urllib.request

from scripts.release_distribution_contract.document_schema import decode, validate
from scripts.feed_signature import check
from scripts.feed_signing import verify
from scripts.update_keys import generation, load_registry


def read(url, maximum):
    request = urllib.request.Request(url, headers={"Cache-Control": "no-cache, no-store",
                                                 "User-Agent": "painted-wolf-release/1"})
    with urllib.request.urlopen(request, timeout=30) as response:
        raw = response.read(maximum + 1)
    if len(raw) > maximum:
        raise ValueError("website distribution metadata exceeds its bound")
    return raw


def public_downloads(receipt):
    version, expected = receipt["version"], receipt["distribution_sha256"]
    if version is None:
        if expected is not None or receipt["manifest_sha256"] is not None:
            raise ValueError("withdrawn website state cannot retain public release digests")
        return []
    if not isinstance(expected, str) or not re.fullmatch("[a-f0-9]{64}", expected):
        raise ValueError("website requires an independent distribution digest")
    from scripts.release_semver import parse
    parse(version)
    base = "https://downloads.paintedwolf.dev/releases/v" + version + "/distribution.v1.json"
    raw = read(base, 2 << 20)
    signature = read(base + ".sig", 65536).decode()
    if hashlib.sha256(raw).hexdigest() != expected:
        raise ValueError("website distribution differs from its independently selected digest")
    document = verify_metadata(raw, signature, version, receipt["manifest_sha256"])
    return sorted(package["url"] for row in document["platforms"].values()
                  for package in row["packages"].values())


def verify_metadata(raw, signature, version, manifest_sha256):
    document = decode(raw)
    validate(document)
    if (document["mode"] != "production" or document["version"] != version
            or document["updater_manifest_sha256"] != manifest_sha256):
        raise ValueError("website distribution differs from the selected release")
    registry = load_registry()
    with tempfile.TemporaryDirectory(prefix="website-distribution-") as temporary:
        path = Path(temporary) / "distribution.v1.json"
        path.write_bytes(raw)
        number = document["signing_generation"]
        check(signature, file=path.name, version=version, number=number, registry=registry)
        verify(path, signature, generation(registry, number)["feed_public_key"])
    return document

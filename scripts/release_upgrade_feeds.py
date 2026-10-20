"""Authenticate and retain the exact public pointers used by upgrade rehearsals."""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import subprocess
import urllib.error
import urllib.request

import feed_signature
import feed_signing
from release_distribution import PUBLIC_READ_HEADERS
from update_keys import generation, validate_binding

ROOT = Path(__file__).resolve().parent.parent
ORIGIN = "https://downloads.paintedwolf.dev"


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def read_bytes(url: str, limit: int) -> bytes | None:
    request = urllib.request.Request(url, headers=PUBLIC_READ_HEADERS)
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            if response.geturl() != url:
                raise ValueError("upgrade evidence must not follow redirects")
            raw = response.read(limit + 1)
            if len(raw) > limit:
                raise ValueError("upgrade evidence exceeds its size bound")
            return raw
    except urllib.error.HTTPError as error:
        if error.code == 404:
            return None
        raise


def capture(base: str, channel: str, number: int, registry: dict, destination: Path) -> dict | None:
    relative = f"updates/{channel}/key-{number}/latest.json"
    url = f"{base}/{relative}"
    raw = read_bytes(url, 1024 * 1024)
    if raw is None:
        return None
    signature = read_bytes(url + ".sig", 16 * 1024)
    if signature is None:
        raise ValueError("published upgrade pointer signature is missing")
    manifest = json.loads(raw)
    validate_binding(registry, manifest, number)
    path = destination / relative
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(raw)
    path.with_suffix(".json.sig").write_bytes(signature)
    document = signature.decode("utf-8")
    feed_signature.check(document, file="latest.json", version=manifest["version"], number=number, registry=registry)
    feed_signing.verify(path, document, generation(registry, number)["feed_public_key"])
    subprocess.run(["bash", str(ROOT / "scripts/release-validate-updater-manifest.sh"),
                    "--file", str(path), "--existing"],
                   env={**os.environ, "DOWNLOAD_BASE_URL": base}, check=True)
    return {"channel": channel, "generation": number, "path": relative,
            "manifest_sha256": digest(raw), "signature_sha256": digest(signature), "manifest": manifest}

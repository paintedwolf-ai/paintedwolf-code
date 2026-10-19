"""Existing R2 release transport and public byte/cache-role verification."""

import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import time
import urllib.error
import urllib.request
import urllib.parse

ROOT = Path(__file__).resolve().parents[2]


def rehearsal_environment() -> str:
    names = {"CLOUDFLARE_ACCOUNT_ID": "RELEASE_TEST_R2_ACCOUNT_ID",
             "CLOUDFLARE_API_TOKEN": "RELEASE_TEST_R2_API_TOKEN",
             "R2_BUCKET": "RELEASE_TEST_R2_BUCKET"}
    required = [*names.values(), "RELEASE_TEST_DOWNLOAD_BASE_URL"]
    if any(not os.environ.get(name) for name in required):
        raise ValueError("explicit RELEASE_TEST_R2_* credentials and test download origin are required")
    origin = os.environ["RELEASE_TEST_DOWNLOAD_BASE_URL"].rstrip("/")
    parsed = urllib.parse.urlsplit(origin)
    if (parsed.scheme != "https" or not parsed.netloc or parsed.path or parsed.query
            or parsed.fragment or parsed.username):
        raise ValueError("test download base URL must be an HTTPS origin")
    for destination, source in names.items():
        os.environ[destination] = os.environ[source]
    return origin


def prefix(value: str, rehearsal: bool):
    path = PurePosixPath(value)
    if (not value or path.is_absolute() or path.as_posix() != value or ".." in path.parts
            or any(not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", part) for part in path.parts)
            or (rehearsal and (len(path.parts) < 2 or path.parts[0] != "release-system-tests"))):
        raise ValueError("repository storage prefix is invalid for the selected trust mode")


def public_bytes(url: str, source: Path, mutable=False):
    local = hashlib.sha256()
    with source.open("rb") as stream:
        while chunk := stream.read(1 << 20):
            local.update(chunk)
    expected = local.hexdigest()
    size = source.stat().st_size
    last = None
    for attempt in range(12):
        try:
            request = urllib.request.Request(url, headers={"Cache-Control": "no-cache, no-store",
                                                           "User-Agent": "painted-wolf-release/1"})
            with urllib.request.urlopen(request, timeout=30) as response:
                control = response.headers.get("Cache-Control", "").lower()
                if (mutable and "no-store" not in control) or (not mutable and "immutable" not in control):
                    raise ValueError("repository public cache policy differs from its object role")
                value, measured = hashlib.sha256(), 0
                while chunk := response.read(1 << 20):
                    measured += len(chunk)
                    if measured > size:
                        raise ValueError("repository public object exceeds its selected size")
                    value.update(chunk)
                if measured == size and value.hexdigest() == expected:
                    return
                last = "public repository bytes differ from selected final bytes"
        except (OSError, ValueError, urllib.error.URLError) as exc:
            last = type(exc).__name__
        if attempt < 11:
            time.sleep(5)
    raise ValueError("repository public object did not converge: " + str(last))


def current(key: str, directory: Path) -> dict | None:
    output = directory / "remote-current.json"
    status = subprocess.check_output(["python3", str(ROOT / "scripts/release_distribution.py"),
                                      "storage-read", "--key", key, "--output", str(output)], text=True).strip()
    if status == "404":
        return None
    if status != "200":
        raise ValueError("cannot establish authenticated repository activation state")
    return json.loads(output.read_text())


def put(key: str, source: Path, mutable=False):
    if mutable:
        subprocess.run(["bunx", "wrangler@4.115.0", "r2", "object", "put", os.environ["R2_BUCKET"] + "/" + key,
                        "--file", str(source), "--content-type", "text/plain",
                        "--cache-control", "no-cache, no-store, must-revalidate", "--remote"], check=True)
    else:
        subprocess.run(["bash", str(ROOT / "scripts/release-r2-immutable-put.sh"), "--key", key,
                        "--file", str(source)], check=True)



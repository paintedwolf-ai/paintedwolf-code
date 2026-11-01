"""Prepare an isolated Swift compiler for the native MLX comparison."""

import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import urllib.request

VERSION = "6.3.3"
EXPECTED_SHA256 = "ee82e57774d6650f94aa06302435d6f44a055b9411698db8ecb85d9a3bcc91d0"
URL = "https://download.swift.org/swift-6.3.3-release/xcode/swift-6.3.3-RELEASE/swift-6.3.3-RELEASE-osx.pkg"


def configured_executable():
    value = os.environ.get("PW_GENERATION_SWIFT")
    if value is None:
        return None
    path = Path(value)
    if not path.is_absolute() or not path.is_file() or not os.access(path, os.X_OK):
        raise ValueError("PW_GENERATION_SWIFT must name an absolute executable path")
    return path


def executable(cache):
    selected = configured_executable()
    if selected is not None:
        return selected
    return cache / ("swift-" + VERSION) / "usr/bin/swift"


def prepare(cache):
    selected = configured_executable()
    if selected is not None:
        subprocess.run([str(selected), "--version"], check=True)
        return selected
    destination = executable(cache).parents[2]
    if executable(cache).is_file():
        record = json.loads((destination / "source.json").read_text())
        if record["url"] != URL or record["sha256"] != EXPECTED_SHA256:
            raise ValueError("isolated Swift toolchain identity mismatch")
        return executable(cache)
    cache.mkdir(parents=True, exist_ok=True)
    if shutil.disk_usage(cache).free < 10 << 30:
        raise ValueError("insufficient space for isolated Swift preparation")
    with tempfile.TemporaryDirectory(prefix=".swift-", dir=cache) as temporary:
        temporary = Path(temporary)
        package = temporary / "swift.pkg"
        digest, size = hashlib.sha256(), 0
        with urllib.request.urlopen(URL, timeout=60) as source, package.open("xb") as output:
            for block in iter(lambda: source.read(1 << 20), b""):
                size += len(block)
                if size > 4 << 30:
                    raise ValueError("Swift toolchain package exceeds its bound")
                digest.update(block)
                output.write(block)
        if digest.hexdigest() != EXPECTED_SHA256:
            raise ValueError("Swift toolchain package digest mismatch")
        subprocess.run(["pkgutil", "--check-signature", str(package)], check=True)
        expanded = temporary / "expanded"
        subprocess.run(["pkgutil", "--expand-full", str(package), str(expanded)], check=True)
        candidates = [p.parent.parent.parent for p in expanded.rglob("usr/bin/swift") if p.is_file()]
        if len(candidates) != 1:
            raise ValueError("Swift package has an unexpected toolchain layout")
        (candidates[0] / "source.json").write_text(json.dumps({"url": URL, "sha256": digest.hexdigest(),
                                                               "package_bytes": size}, indent=2) + "\n")
        candidates[0].rename(destination)
    return executable(cache)


def identity(cache):
    compiler = executable(cache)
    return {
        "executable": str(compiler),
        "version": subprocess.check_output([str(compiler), "--version"], text=True).strip(),
        "sdk": subprocess.check_output(["xcrun", "--show-sdk-path"], text=True).strip(),
        "sdk_version": subprocess.check_output(["xcrun", "--show-sdk-version"], text=True).strip(),
    }

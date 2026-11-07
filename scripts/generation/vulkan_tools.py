"""Prepare the shader compiler and headers without replacing the system loader."""

import fcntl
import hashlib
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import tarfile
import tempfile

VERSION = "1.4.335.0"
CACHE_NAME = "vulkan-tools-" + VERSION + "-cmake"
SHA256 = "ccab8047f33ef848e3928c7fdd19987b35da3085ad20178f473acc230fa3c5f2"
URL = f"https://sdk.lunarg.com/sdk/download/{VERSION}/linux/vulkan_sdk.tar.xz"


def unpack(archive, destination):
    total = 0
    with tarfile.open(archive) as bundle:
        for member in bundle:
            name = PurePosixPath(member.name)
            if name.is_absolute() or ".." in name.parts:
                raise ValueError("unsafe SDK entry")
            if len(name.parts) < 4 or name.parts[:2] != (VERSION, "x86_64"):
                continue
            relative = Path(*name.parts[2:])
            selected = (str(relative) == "bin/glslc" or name.parts[2] == "include"
                        or relative.parts[:2] in {("share", "cmake"), ("lib", "cmake")})
            if not selected or member.isdir():
                continue
            if not member.isfile():
                raise ValueError("SDK tool entry is not a regular file")
            total += member.size
            if total > 128 << 20:
                raise ValueError("SDK tools exceed their size bound")
            target = destination / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            with bundle.extractfile(member) as source, target.open("xb") as output:
                shutil.copyfileobj(source, output)
            target.chmod(0o755 if relative == Path("bin/glslc") else 0o644)
    if not (destination / "bin/glslc").is_file() or not (destination / "include/vulkan/vulkan.h").is_file():
        raise ValueError("SDK lacks the required compiler or headers")


def prepare(cache):
    cache.mkdir(parents=True, exist_ok=True)
    destination = cache / CACHE_NAME
    with (cache / ("vulkan-tools-" + VERSION + ".lock")).open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        identity = destination / ".sha256"
        if identity.is_file() and identity.read_text().strip() == SHA256:
            return destination
        with tempfile.TemporaryDirectory(prefix=".vulkan-", dir=cache) as temporary:
            root = Path(temporary)
            archive = root / "sdk.tar.xz"
            subprocess.run(["curl", "--fail", "--location", "--silent", "--show-error",
                            "--max-filesize", str(512 << 20), "--output", str(archive), URL], check=True)
            digest = hashlib.sha256()
            with archive.open("rb") as source:
                for block in iter(lambda: source.read(1 << 20), b""):
                    digest.update(block)
            if digest.hexdigest() != SHA256:
                raise ValueError("Vulkan SDK digest mismatch")
            tree = root / "tools"
            tree.mkdir()
            unpack(archive, tree)
            (tree / ".sha256").write_text(SHA256 + "\n")
            tree.rename(destination)
    return destination

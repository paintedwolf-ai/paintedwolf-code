"""Prepare the shader compiler and headers without replacing the system loader."""

import fcntl
import hashlib
from pathlib import Path, PurePosixPath
import platform
import shutil
import subprocess
import tarfile
import tempfile

VERSION = "1.4.335.0"
CACHE_NAME = "vulkan-tools-" + VERSION + "-cmake"
SHA256 = "ccab8047f33ef848e3928c7fdd19987b35da3085ad20178f473acc230fa3c5f2"
URL = f"https://sdk.lunarg.com/sdk/download/{VERSION}/linux/vulkan_sdk.tar.xz"
# LunarG publishes Linux SDK archives only for x86_64, so aarch64 hosts reuse
# the archive's headers and CMake metadata and take glslc from the system.
ARCHIVE_ARCH = "x86_64"
AARCH64 = "aarch64"
AARCH64_MACHINES = frozenset({"aarch64", "arm64"})
X86_64_MACHINES = frozenset({"x86_64", "amd64"})


def host_arch():
    machine = platform.machine().lower()
    if machine in X86_64_MACHINES:
        return ARCHIVE_ARCH
    if machine in AARCH64_MACHINES:
        return AARCH64
    raise ValueError(f"unsupported host architecture for the Vulkan tools: {machine}")


def identity(arch):
    # The x86_64 identity stays the bare archive digest; aarch64 appends a
    # variant marker so a provisioned cache never crosses architectures.
    return SHA256 if arch == ARCHIVE_ARCH else f"{SHA256}:{arch}"


def unpack(archive, destination, arch):
    total = 0
    with tarfile.open(archive) as bundle:
        for member in bundle:
            name = PurePosixPath(member.name)
            if name.is_absolute() or ".." in name.parts:
                raise ValueError("unsafe SDK entry")
            if len(name.parts) < 4 or name.parts[:2] != (VERSION, ARCHIVE_ARCH):
                continue
            relative = Path(*name.parts[2:])
            selected = ((arch == ARCHIVE_ARCH and str(relative) == "bin/glslc")
                        or name.parts[2] == "include"
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
    if not (destination / "include/vulkan/vulkan.h").is_file() or (
            arch == ARCHIVE_ARCH and not (destination / "bin/glslc").is_file()):
        raise ValueError("SDK lacks the required compiler or headers")


def provision_glslc(tree):
    source = shutil.which("glslc")
    if source is None:
        raise ValueError("no system glslc found; aarch64 hosts need the glslc package "
                         "because LunarG publishes no arm64 Linux SDK")
    target = tree / "bin/glslc"
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, target)
    target.chmod(0o755)


def verify_glslc(path):
    try:
        result = subprocess.run([str(path), "--version"], capture_output=True, text=True,
                                errors="replace", check=False)
    except OSError as error:
        raise ValueError(f"the Vulkan shader compiler at {path} cannot be executed on this host "
                         f"({error}); the archive compiler is x86_64-only, so aarch64 hosts must "
                         "provision the system glslc package") from error
    if result.returncode != 0:
        detail = (result.stderr or result.stdout).strip()
        raise ValueError(f"the Vulkan shader compiler at {path} failed --version "
                         f"(exit {result.returncode}): {detail}")


def trusted(destination, expected):
    identity_file = destination / ".sha256"
    if not identity_file.is_file() or identity_file.read_text().strip() != expected:
        return False
    glslc = destination / "bin/glslc"
    if not glslc.is_file():
        return False
    try:
        verify_glslc(glslc)
    except ValueError:
        return False
    return True


def prepare(cache):
    cache.mkdir(parents=True, exist_ok=True)
    destination = cache / CACHE_NAME
    arch = host_arch()
    with (cache / ("vulkan-tools-" + VERSION + ".lock")).open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        expected = identity(arch)
        if trusted(destination, expected):
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
            unpack(archive, tree, arch)
            if arch != ARCHIVE_ARCH:
                provision_glslc(tree)
            verify_glslc(tree / "bin/glslc")
            (tree / ".sha256").write_text(expected + "\n")
            if destination.is_dir():
                shutil.rmtree(destination)
            elif destination.exists():
                destination.unlink()
            tree.rename(destination)
    return destination

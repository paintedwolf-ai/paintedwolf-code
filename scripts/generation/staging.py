"""Stage authenticated generation resources into an application build."""

import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

from provenance import file_digest, verified_build

MODEL = "gemma4-e4b-paintedwolf"
NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}\Z")


def copy_file(source, destination, executable=False):
    if source.is_symlink() or not source.is_file():
        raise ValueError(f"generation input is not a regular file: {source}")
    shutil.copyfile(source, destination)
    destination.chmod(0o755 if executable else 0o644)


def publish_file(source, destination, executable=False):
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".generation-", dir=destination.parent) as temporary:
        staged = Path(temporary) / destination.name
        copy_file(source, staged, executable)
        os.replace(staged, destination)


def stage_runtime(build, binary, destination, resources, backend):
    if backend not in {"mlx", "vulkan"}:
        raise ValueError("generation backend must be mlx or vulkan")
    receipt = verified_build("native", build, binary)
    companion = build / "metal/mlx.metallib" if backend == "mlx" else binary.parent / "libnativegen_vulkan.so"
    expected = receipt["metallib_sha256" if backend == "mlx" else "adapter_sha256"]
    target = resources / "mlx.metallib" if backend == "mlx" else destination.parent / companion.name
    publish_file(companion, target)
    publish_file(binary, destination, executable=True)
    if file_digest(destination) != receipt["binary_sha256"] or file_digest(target) != expected:
        raise ValueError("generation inputs changed during staging")
    if backend == "vulkan":
        installed = resources / companion.name
        publish_file(target, installed)
        if file_digest(installed) != expected:
            raise ValueError("Vulkan adapter changed during staging")
    return receipt


def verify_seed(verifier, bundle, authority, backend):
    raw = subprocess.check_output([str(verifier), "-bundle", str(bundle), "-authority", str(authority),
                                   "-model", MODEL, "-device", backend])
    return json.loads(raw)


def stage_seed(verifier, bundle, authority, resources, backend):
    """Publish only named signed payloads, after verifying the copied bytes."""
    if backend not in {"mlx", "vulkan"}:
        raise ValueError("generation backend must be mlx or vulkan")
    signed = verify_seed(verifier, bundle, authority, backend)
    manifest = signed["manifest"]
    names = [entry.get("name", "") for entry in [manifest["artifact"], *manifest.get("files", [])]]
    if any(not NAME.fullmatch(name) or name == "manifest.json" for name in names) or len(set(names)) != len(names):
        raise ValueError("packaged generation artifacts require unique explicit filenames")
    resources.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".seed-", dir=resources) as temporary:
        staged = Path(temporary)
        keys = staged / "release-keys.json"
        copy_file(authority, keys)
        payload = staged / backend
        payload.mkdir()
        for name in ["manifest.json", *names]:
            copy_file(bundle / name, payload / name)
        if verify_seed(verifier, payload, keys, backend) != signed:
            raise ValueError("generation manifest changed during staging")
        models = resources / "models"
        models.mkdir(exist_ok=True)
        destination = models / backend
        # Build resources have no live model leases; the host imports its own revision.
        if destination.exists():
            if destination.is_symlink() or not destination.is_dir():
                raise ValueError("generation staging destination is not a directory")
            shutil.rmtree(destination)
        os.replace(payload, destination)
        os.replace(keys, resources / "release-keys.json")
    return signed

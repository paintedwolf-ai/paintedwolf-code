"""Bind native probes to final package bytes and the actual installed executable images."""
from __future__ import annotations

import base64
import hashlib
import json
import os
from pathlib import Path
import platform
import stat
import struct
import subprocess
import tempfile

from . import installed_evidence as evidence
from .installed_cases.package_payload import inspect, stable, verify

ARCHES = {"x86_64": ("amd64", 62), "aarch64": ("arm64", 183)}
ROLES = {"painted-wolf-code", "pw", "pw-logs", "pw-vault-identity", "pw-document-core", "bialy"}


def file_identity(path: Path) -> dict:
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, "rb") as stream:
        before = os.fstat(stream.fileno())
        if not stat.S_ISREG(before.st_mode):
            raise ValueError("installed runtime requires a regular file")
        digest = hashlib.sha256()
        header = stream.read(64)
        digest.update(header)
        for block in iter(lambda: stream.read(1 << 20), b""):
            digest.update(block)
        if stable(before) != stable(os.fstat(stream.fileno())) or stable(before) != stable(path.lstat()):
            raise ValueError("installed runtime file changed during inspection")
    return {"sha256": digest.hexdigest(), "bytes": before.st_size, "mode": stat.S_IMODE(before.st_mode),
            "device": before.st_dev, "inode": before.st_ino, "header": header}


def elf(path: Path, architecture: str) -> dict:
    observed = file_identity(path)
    machine = {"amd64": 62, "arm64": 183}[architecture]
    header = observed.pop("header")
    if (len(header) < 64 or header[:6] != b"\x7fELF\x02\x01"
            or struct.unpack_from("<H", header, 16)[0] not in (2, 3)
            or struct.unpack_from("<H", header, 18)[0] != machine
            or observed["mode"] & 0o111 == 0 or observed["mode"] & 0o7000):
        raise ValueError("installed runtime image is not an ordinary executable for this native CPU")
    return observed


def manifest(root: Path, architecture: str) -> dict:
    path = root / "manifest.json"
    if path.is_symlink() or path.stat().st_size > 4 << 20:
        raise ValueError("installed manifest is not a bounded physical file")
    record = evidence.object_fields(json.loads(path.read_text()), {
        "format_version", "package", "version", "architecture", "install_root", "executables"})
    if (type(record["format_version"]) is not int or record["format_version"] != 1
            or record["package"] != "painted-wolf-code"
            or record["install_root"] != str(root) or not record["version"]
            or record["architecture"] != {"amd64": "x86_64", "arm64": "aarch64"}[architecture]
            or not isinstance(record["executables"], list) or not record["executables"]):
        raise ValueError("installed manifest differs from the selected native layout")
    observed = {}
    roles = {root / "bin" / role for role in ROLES}
    entry_points = {Path("/usr/bin") / role for role in ("painted-wolf-code", "pw", "pw-logs")}
    for raw in record["executables"]:
        item = evidence.object_fields(raw, {"path", "sha256", "size"})
        image = Path(item["path"])
        allowed = image.is_relative_to(root) or root == Path("/opt/painted-wolf-code") and image in entry_points
        if (not image.is_absolute() or str(image) != item["path"] or not allowed or image in observed
                or ".." in image.parts or type(item["size"]) is not int or item["size"] < 0):
            raise ValueError("installed manifest has a duplicate, foreign or invalid image")
        if image in roles:
            identity = elf(image, architecture)
        else:
            identity = file_identity(image)
            header = identity.pop("header")
            if header.startswith(b"\x7fELF"):
                identity = elf(image, architecture)
            elif not header.startswith(b"#!") or identity["mode"] & 0o111 == 0 or identity["mode"] & 0o7000:
                raise ValueError("installed manifest executable has an unsupported format")
        if (identity["sha256"], identity["bytes"]) != (item["sha256"], item["size"]):
            raise ValueError("installed executable differs from its sealed manifest")
        observed[image] = identity
    if not roles <= observed.keys():
        raise ValueError("installed manifest omits a required native helper")
    return {"version": record["version"], "images": {str(path): value for path, value in observed.items()}}


def nix_command(program: Path, *arguments):
    if not program.is_absolute() or program.is_symlink():
        raise ValueError("Nix verification requires an explicit physical client")
    with tempfile.TemporaryDirectory(prefix="paintedwolf-installed-nix-") as config:
        result = subprocess.run([str(program), "--extra-experimental-features", "nix-command",
            "--option", "substituters", "", "--option", "extra-substituters", "",
            "--option", "builders", "", "--store", "daemon", *arguments],
            env={"HOME": config, "NIX_CONF_DIR": config, "NIX_USER_CONF_FILES": "/dev/null", "LC_ALL": "C"},
            capture_output=True, timeout=120)
    if result.returncode != 0:
        raise ValueError("actual Nix output verification failed")
    if len(result.stdout) > 16 << 20:
        raise ValueError("Nix verification output exceeds its bound")
    return result.stdout


def bind(final: dict, source: str, artifact_id: str, file_root: Path, install_root: Path,
         nix_program: Path | None = None) -> dict:
    artifacts = evidence.artifacts(final, evidence.source(source))
    if artifact_id not in artifacts:
        raise ValueError("installed runtime does not identify a final artifact")
    artifact = artifacts[artifact_id]
    native = ARCHES.get(platform.machine())
    if platform.system() != "Linux" or native is None or artifact["architecture"] != native[0]:
        raise ValueError("actual installed execution requires its matching native Linux CPU")
    root = install_root.resolve(strict=True)
    if root != install_root or not root.is_absolute():
        raise ValueError("installed runtime requires its physical installation root")
    identity = artifact["identity"]
    if identity["kind"] == "file":
        if root != Path("/opt/painted-wolf-code"):
            raise ValueError("native packages require their actual installed /opt layout")
        entries = inspect(file_root / artifact_id, artifact["installation"], identity["sha256"])
        required = {str((root / "bin" / role).relative_to("/")) for role in ROLES}
        required.add(str((root / "manifest.json").relative_to("/")))
        if not required <= entries.keys():
            raise ValueError("final package omits the installed manifest or a required helper")
        payload = verify(entries)
    else:
        output = Path(identity["store_path"])
        if root != output / "lib/painted-wolf-code" or nix_program is None:
            raise ValueError("Nix execution requires its exact final output and explicit client")
        client = elf(nix_program, native[0])
        nix_command(nix_program, "store", "verify", "--recursive", "--no-trust", str(output))
        rows = json.loads(nix_command(nix_program, "path-info", "--json", "--json-format", "1", str(output)))
        sri = "sha256-" + base64.b64encode(bytes.fromhex(identity["sha256"])).decode()
        if not isinstance(rows, dict) or set(rows) != {str(output)} or rows[str(output)].get("narHash") != sri:
            raise ValueError("actual installed output differs from the selected final NAR")
        if elf(nix_program, native[0]) != client:
            raise ValueError("Nix client changed while verifying the installed output")
        payload = {"status": "passed", "store_path": str(output), "nar_hash": sri,
                   "nix_client": {"path": str(nix_program), **client},
                   "content_verified": True, "publisher_or_custody_authority": False}
    images = manifest(root, native[0])
    if identity["kind"] == "file" and any(str(Path(path).relative_to("/")) not in entries for path in images["images"]):
        raise ValueError("installed manifest names an executable outside the selected final package")
    launcher = Path("/usr/bin/painted-wolf-code") if identity["kind"] == "file" else output / "bin/painted-wolf-code"
    if identity["kind"] == "file":
        if str(launcher) not in images["images"]:
            raise ValueError("final package manifest omits its public application launcher")
        launcher_image = images["images"][str(launcher)]
    else:
        launcher_image = file_identity(launcher)
        header = launcher_image.pop("header")
        if launcher_image["mode"] & 0o111 == 0 or launcher_image["mode"] & 0o7000 or not header.startswith((b"#!", b"\x7fELF")):
            raise ValueError("final Nix output has no ordinary public application launcher")
    return {"format_version": 1, "source_commit": source, "artifact": artifact,
            "install_root": str(root), "payload": payload, "launcher": {"path": str(launcher), **launcher_image}, **images}


def unchanged(binding: dict, role: str) -> Path:
    if role not in ROLES:
        raise ValueError("native probe requested an unknown executable role")
    path = Path(binding["install_root"]) / "bin" / role
    if elf(path, binding["artifact"]["architecture"]) != binding["images"][str(path)]:
        raise ValueError("installed executable changed since its final artifact was verified")
    return path

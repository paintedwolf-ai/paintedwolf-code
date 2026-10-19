"""Collect read-only ALPM ownership facts for installed-package qualification."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import stat

FIELD = re.compile(r"%([A-Z][A-Z0-9_]*)%\Z")


def sections(data: bytes) -> dict[str, list[str]]:
    result, active = {}, None
    for line in data.decode("utf-8").splitlines():
        if not line:
            active = None
            continue
        header = FIELD.fullmatch(line)
        if header:
            active = header[1]
            if active in result:
                raise ValueError("ALPM record repeats a field")
            result[active] = []
        elif active is None:
            raise ValueError("ALPM value is outside a declared field")
        else:
            result[active].append(line)
    return result


def single(fields: dict, key: str) -> str:
    values = fields.get(key)
    if not isinstance(values, list) or len(values) != 1 or not values[0]:
        raise ValueError(f"ALPM record requires one {key}")
    return values[0]


def read(path: Path, limit=16 << 20) -> bytes:
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), "rb") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
            raise ValueError("ALPM evidence must be a bounded regular file")
        data = source.read(limit + 1)
    if len(data) > limit:
        raise ValueError("ALPM evidence exceeds its size bound")
    return data


def executable_identity(path: Path, entry: dict) -> dict:
    size, mode = entry["size"], entry["mode"]
    if (type(size) is not int or not 0 <= size <= 2 << 30 or type(mode) is not int
            or not 0 <= mode <= 0o7777 or not mode & 0o111
            or not re.fullmatch(r"[0-9a-f]{64}", entry["sha256"])):
        raise ValueError("selected executable manifest has an invalid identity")
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as source:
        before = os.fstat(source.fileno())
        if not stat.S_ISREG(before.st_mode) or before.st_size != size or stat.S_IMODE(before.st_mode) != mode:
            raise ValueError("installed executable size or mode differs from the selected final manifest")
        digest, measured = hashlib.sha256(), 0
        while chunk := source.read(min(1 << 20, size + 1 - measured)):
            digest.update(chunk)
            measured += len(chunk)
            if measured > size:
                raise ValueError("installed executable grew while evidence was collected")
        after, named = os.fstat(source.fileno()), path.lstat()
    def identity(info):
        return (info.st_dev, info.st_ino, info.st_mode, info.st_size, info.st_mtime_ns, info.st_ctime_ns)
    if identity(before) != identity(after) or identity(after) != identity(named):
        raise ValueError("installed executable changed while evidence was collected")
    if digest.hexdigest() != entry["sha256"] or measured != size:
        raise ValueError("installed executable differs from the selected final manifest")
    return {"path": entry["path"], "sha256": digest.hexdigest(), "size": size, "mode": mode}


def installed(database: Path, package: str, expected_version: str, architecture: str,
              executables: list[dict], root: Path = Path("/")) -> dict:
    """Verify selected installed bytes and retain the corresponding ALPM records."""
    if architecture not in {"x86_64", "aarch64"} or not executables:
        raise ValueError("installed evidence requires a supported architecture and executable manifest")
    lock = database / "db.lck"
    if lock.exists():
        raise ValueError("package transaction is active; installed evidence is unverified")
    local = database / "local"
    version = read(local / "ALPM_DB_VERSION", 128)
    if version.strip() not in {b"9"}:
        raise ValueError("unsupported local ALPM database version")
    directories = sorted(path.name for path in local.iterdir() if path.is_dir())
    matching = []
    for name in directories:
        path = local / name / "desc"
        data = read(path)
        fields = sections(data)
        if single(fields, "NAME") == package:
            matching.append((path, data, fields))
    if len(matching) != 1:
        raise ValueError("selected package must have exactly one installed ALPM record")
    descriptor, description, fields = matching[0]
    if single(fields, "VERSION") != expected_version or single(fields, "ARCH") != architecture:
        raise ValueError("installed package differs from the separately selected version or architecture")
    file_record = descriptor.parent / "files"
    files = read(file_record)
    owned = sections(files).get("FILES")
    if not isinstance(owned, list) or len(set(owned)) != len(owned):
        raise ValueError("ALPM ownership record is absent or ambiguous")
    for name in owned:
        path = PurePosixPath(name)
        if path.is_absolute() or ".." in path.parts:
            raise ValueError("ALPM ownership record contains an unsafe path")
    measured, seen = [], set()
    for entry in executables:
        name = entry["path"]
        if (not name.startswith("/") or name.startswith("//") or PurePosixPath(name).as_posix() != name
                or ".." in PurePosixPath(name).parts or name in seen):
            raise ValueError("selected executable manifest contains an invalid or repeated path")
        seen.add(name)
        if name[1:] not in owned:
            raise ValueError(f"selected executable is not owned by the package: {name}")
        path = root / name[1:]
        if not stat.S_ISREG(path.lstat().st_mode):
            raise ValueError(f"selected executable is not an executable regular file: {name}")
        measured.append(executable_identity(path, entry))
    if (lock.exists() or read(descriptor) != description or read(file_record) != files
            or read(local / "ALPM_DB_VERSION", 128) != version
            or sorted(path.name for path in local.iterdir() if path.is_dir()) != directories):
        raise ValueError("ALPM database changed while evidence was collected")
    return {"format_version": 1, "package": package, "version": expected_version,
            "architecture": architecture, "database_format": 9,
            "desc_sha256": hashlib.sha256(description).hexdigest(),
            "files_sha256": hashlib.sha256(files).hexdigest(),
            "validation_record": fields.get("VALIDATION", []), "executables": measured,
            "package_signature_verified": False, "desktop_qualified": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--database", type=Path, default=Path("/var/lib/pacman"))
    parser.add_argument("--package", default="painted-wolf-code")
    parser.add_argument("--version", required=True)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        if platform.system() != "Linux":
            raise ValueError("installed pacman facts require a Linux system")
        destination = args.output.resolve()
        if destination == args.manifest.resolve() or destination.is_relative_to(args.database.resolve()):
            raise ValueError("evidence output must be separate from selected inputs and the ALPM database")
        manifest = json.loads(args.manifest.read_text())
        result = installed(args.database, args.package, args.version, platform.machine(), manifest["executables"])
        with args.output.open("x") as output:
            output.write(json.dumps(result, indent=2) + "\n")
    except (OSError, ValueError, KeyError) as exc:
        parser.exit(1, f"Installed pacman facts remain unverified: {exc}\n")


if __name__ == "__main__":
    main()

"""Fixed native package operations on the disposable qualification guest."""
from __future__ import annotations

import hashlib
import os
from pathlib import Path
import re
import stat

from .records import CaseUnavailable

def current_package(binding: dict) -> Path:
    inputs = binding.get("case_inputs", {}).get("native_packages")
    if (not isinstance(inputs, dict) or not {"format_version", "current_package"} <= set(inputs)
            or set(inputs) - {"format_version", "current_package", "installation"}
            or type(inputs["format_version"]) is not int or inputs["format_version"] != 1):
        raise CaseUnavailable("native_packages requires format_version:1 and current_package")
    if "installation" in inputs:
        operation = inputs["installation"]
        if operation != {"operation": "fresh-install", "expected_absent": True} or type(operation.get("expected_absent")) is not bool:
            raise ValueError("native package installation requires its exact fresh operation")
    value = inputs["current_package"]
    if not isinstance(value, str):
        raise ValueError("native package path must be a string")
    path = Path(value)
    if not path.is_absolute() or path.name != binding["artifact"]["id"] or path.resolve(strict=True) != path:
        raise ValueError("native package path differs from selected final artifact")
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o022:
        raise ValueError("native package must be a physical non-writable regular file")
    digest = hashlib.sha256()
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), "rb") as stream:
        opened = os.fstat(stream.fileno())
        if (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns) != (opened.st_dev, opened.st_ino, opened.st_size, opened.st_mtime_ns):
            raise ValueError("native package changed before opening")
        for block in iter(lambda: stream.read(1 << 20), b""):
            digest.update(block)
        after = os.fstat(stream.fileno())
    if (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns) != (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns) or digest.hexdigest() != binding["artifact"]["identity"]["sha256"]:
        raise ValueError("native package bytes changed from selected final artifact")
    return path


def operations(family: str, package: Path, owner: str) -> tuple[list[str], list[str]]:
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9+._-]*", owner):
        raise ValueError("native package ownership has an invalid name")
    commands = {
        "deb": (["dpkg", "--remove", owner], ["dpkg", "--install", str(package)]),
        "rpm": (["rpm", "--erase", owner], ["rpm", "--install", str(package)]),
        "pacman": (["pacman", "--remove", "--noconfirm", owner], ["pacman", "--upgrade", "--noconfirm", str(package)]),
    }
    if family not in commands:
        raise CaseUnavailable("native package manager is not selected")
    return tuple(["sudo", "-n", *arguments] for arguments in commands[family])


def owner_commands(family: str, package: Path, launcher: str) -> tuple[list[str], list[str]]:
    commands = {
        "deb": (["dpkg-query", "-S", launcher], ["dpkg-deb", "-f", str(package), "Package"]),
        "rpm": (["rpm", "-qf", "--qf", "%{NAME}", launcher], ["rpm", "-qp", "--qf", "%{NAME}", str(package)]),
        "pacman": (["pacman", "-Qqo", launcher], ["pacman", "-Qp", "--print-format", "%n", str(package)]),
    }
    if family not in commands:
        raise CaseUnavailable("native package manager is not selected")
    return commands[family]

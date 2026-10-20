"""Retain and extract one digest-bound controller artifact without executing inputs."""

from __future__ import annotations

import hashlib
import os
from pathlib import Path, PurePosixPath
import shutil
import stat
import subprocess
import tempfile
import zipfile

MAX_BYTES = 64 << 30
MAX_FILES = 100_000


def extract(archive: Path, destination: Path, expected_sha256: str) -> None:
    if destination.exists() or destination.is_symlink():
        raise ValueError("campaign extraction requires a fresh destination")
    digest = hashlib.sha256()
    with archive.open("rb") as source:
        for block in iter(lambda: source.read(1 << 20), b""):
            digest.update(block)
    if digest.hexdigest() != expected_sha256:
        raise ValueError("downloaded campaign differs from its server artifact digest")
    with zipfile.ZipFile(archive) as source:
        rows = source.infolist()
        if not rows or len(rows) > MAX_FILES:
            raise ValueError("campaign archive has an invalid entry count")
        names, total = {}, 0
        for row in rows:
            name = row.filename.rstrip("/") if row.is_dir() else row.filename
            path = PurePosixPath(name)
            kind = stat.S_IFMT(row.external_attr >> 16)
            if (not name or len(name) > 1000 or "\\" in name or path.is_absolute()
                    or str(path) != name or ".." in path.parts or name in names
                    or kind not in ({0, stat.S_IFDIR} if row.is_dir() else {0, stat.S_IFREG})
                    or row.flag_bits & 1 or row.file_size < 0 or row.file_size > 16 << 30):
                raise ValueError("campaign archive contains an unsafe or duplicate entry")
            names[name] = row.is_dir()
            total += row.file_size
        if total > MAX_BYTES:
            raise ValueError("campaign archive exceeds its size bound")
        for name in names:
            if any(parent.as_posix() in names and not names[parent.as_posix()]
                   for parent in PurePosixPath(name).parents if parent.as_posix() != "."):
                raise ValueError("campaign archive places an entry beneath a file")
        if shutil.disk_usage(destination.parent).free < total + (1 << 30):
            raise ValueError("insufficient free disk for the retained campaign")
        destination.mkdir(mode=0o700)
        try:
            for row in rows:
                path = destination / row.filename
                if row.is_dir():
                    path.mkdir(mode=0o700, parents=True, exist_ok=True)
                    continue
                path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                with source.open(row) as incoming, path.open("xb") as output:
                    # ZipExtFile enforces declared length and CRC while streaming.
                    shutil.copyfileobj(incoming, output, length=1 << 20)
                path.chmod(0o600)
        except BaseException:
            shutil.rmtree(destination)
            raise


def download(selected: dict, destination: Path) -> None:
    if shutil.disk_usage(destination.parent).free < selected["artifact_bytes"] + (1 << 30):
        raise ValueError("insufficient free disk for the selected campaign download")
    with tempfile.TemporaryDirectory(prefix=".linux-campaign-", dir=destination.parent) as temporary:
        archive = Path(temporary) / "campaign.zip"
        endpoint = (f"repos/{selected['repository']}/actions/artifacts/"
                    f"{selected['artifact_id']}/zip")
        with archive.open("xb") as output:
            subprocess.run(["gh", "api", endpoint], stdout=output, check=True, timeout=1800)
        extract(archive, destination, selected["artifact_sha256"])


def retain_tree(source: Path, destination: Path, *, root_files: set[str], root_dirs: set[str]) -> None:
    """Copy bounded regular files through pinned directory descriptors."""
    if (source.is_symlink() or not source.is_dir() or destination.exists() or destination.is_symlink()
            or destination.resolve().is_relative_to(source.resolve())):
        raise ValueError("campaign production requires a fresh independent destination")
    count, total = 0, 0
    destination.mkdir(mode=0o700)
    try:
        for parent, directories, files, descriptor in os.fwalk(source, follow_symlinks=False):
            relative = Path(parent).relative_to(source)
            if relative == Path("."):
                if set(files) != root_files or not set(directories) <= root_dirs:
                    raise ValueError("campaign input root contains missing or unexpected entries")
            output = destination / relative
            output.mkdir(mode=0o700, exist_ok=True)
            for name in directories:
                count += 1
                if count > MAX_FILES:
                    raise ValueError("campaign exceeds its entry count bound")
                if not stat.S_ISDIR(os.stat(name, dir_fd=descriptor, follow_symlinks=False).st_mode):
                    raise ValueError("campaign directories must not be links or special files")
            for name in files:
                count += 1
                if count > MAX_FILES:
                    raise ValueError("campaign exceeds its file count bound")
                fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=descriptor)
                with os.fdopen(fd, "rb") as incoming:
                    before = os.fstat(incoming.fileno())
                    if not stat.S_ISREG(before.st_mode) or before.st_size > 16 << 30:
                        raise ValueError("campaign requires bounded regular files")
                    total += before.st_size
                    if total > MAX_BYTES or shutil.disk_usage(destination).free < before.st_size + (1 << 30):
                        raise ValueError("campaign exceeds its size or available disk bound")
                    target = output / name
                    with target.open("xb") as outgoing:
                        remaining = before.st_size
                        while remaining:
                            block = incoming.read(min(remaining, 1 << 20))
                            if not block:
                                raise ValueError("campaign file changed during retention")
                            outgoing.write(block)
                            remaining -= len(block)
                        if incoming.read(1):
                            raise ValueError("campaign file grew during retention")
                    target.chmod(0o600)
                    after = os.fstat(incoming.fileno())
                    current = os.stat(name, dir_fd=descriptor, follow_symlinks=False)
                    identity = lambda row: (row.st_dev, row.st_ino, row.st_size, row.st_mtime_ns, row.st_ctime_ns)
                    if identity(before) != identity(after) or identity(after) != identity(current):
                        raise ValueError("campaign file changed or was replaced during retention")
    except BaseException:
        shutil.rmtree(destination)
        raise


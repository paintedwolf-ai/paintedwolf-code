"""Compare final package payload paths, bytes, modes and links with installed files."""
from __future__ import annotations

from dataclasses import dataclass, replace
import hashlib
import os
from pathlib import Path, PurePosixPath
import stat
import subprocess
import tarfile
import tempfile
import threading

MAX_BYTES = 8 << 30
MAX_FILES = 200_000
CHUNK = 1 << 20


@dataclass(frozen=True)
class Entry:
    name: str
    mode: int
    uid: int
    gid: int
    kind: str
    size: int = 0
    sha256: str = ""
    target: str = ""


class MetadataMismatch(ValueError):
    def __init__(self, entry: Entry, observed):
        super().__init__("installed package mode or owner differs from final payload")
        self.facts = {"path": entry.name, "kind": entry.kind,
                      "expected": {"mode": entry.mode, "uid": entry.uid, "gid": entry.gid},
                      "observed": {"mode": stat.S_IMODE(observed.st_mode), "uid": observed.st_uid, "gid": observed.st_gid}}


def name(value: str) -> str:
    value = value.removeprefix("./")
    path = PurePosixPath(value)
    if (not value or path.is_absolute() or str(path) != value
            or ".." in path.parts or "\0" in value or len(value.encode()) > 4096):
        raise ValueError("package payload path must be normalized and relative")
    return value


class BoundedReader:
    def __init__(self, stream):
        self.stream, self.consumed = stream, 0

    def read(self, size=CHUNK):
        if not 0 <= size <= CHUNK:
            raise ValueError("package archive requested oversized metadata")
        data = self.stream.read(size)
        self.consumed += len(data)
        if self.consumed > MAX_BYTES:
            raise ValueError("package payload exceeds its byte bound")
        return data


def content(stream, size: int) -> tuple[str, int]:
    if not 0 <= size <= MAX_BYTES:
        raise ValueError("package member exceeds its byte bound")
    digest, checksum, remaining = hashlib.sha256(), 0, size
    while remaining:
        block = stream.read(min(CHUNK, remaining))
        if not block:
            raise ValueError("package member is truncated")
        digest.update(block)
        checksum = (checksum + sum(block)) & 0xFFFFFFFF
        remaining -= len(block)
    return digest.hexdigest(), checksum


def add(entries: dict, entry: Entry):
    if entry.name in entries or len(entries) >= MAX_FILES:
        raise ValueError("package payload repeats a path or exceeds its entry bound")
    if entry.uid != 0 or entry.gid != 0 or entry.mode & 0o7000:
        raise ValueError("package payload has an unexpected owner or privilege mode")
    entries[entry.name] = entry


def tar_entries(stream) -> dict[str, Entry]:
    entries = {}
    with tarfile.open(fileobj=stream, mode="r|", bufsize=64 << 10) as archive:
        for member in archive:
            if member.name in (".", "./") and member.isdir():
                continue
            path = name(member.name)
            entry = Entry(path, member.mode, member.uid, member.gid, "directory")
            if member.isfile():
                digest, _ = content(archive.extractfile(member), member.size)
                entry = replace(entry, kind="file", size=member.size, sha256=digest)
            elif member.issym():
                if not member.linkname or "\0" in member.linkname:
                    raise ValueError("package symbolic link has no valid target")
                entry = replace(entry, kind="symlink", target=member.linkname)
            elif member.islnk():
                entry = replace(entry, kind="hardlink", target=name(member.linkname))
            elif not member.isdir():
                raise ValueError("package payload contains an unsupported file type")
            add(entries, entry)
    return entries


def exact(stream, size):
    data = stream.read(size)
    if len(data) != size:
        raise ValueError("package cpio header or name is truncated")
    return data


def stable(info):
    return (info.st_dev, info.st_ino, info.st_mode, info.st_uid, info.st_gid,
            info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def cpio_entries(stream) -> dict[str, Entry]:
    entries, links = {}, {}
    while True:
        header = exact(stream, 110)
        if header[:6] not in (b"070701", b"070702"):
            raise ValueError("RPM payload requires the supported newc cpio format")
        if any(byte not in b"0123456789abcdefABCDEF" for byte in header[6:]):
            raise ValueError("RPM cpio header contains an invalid integer")
        try:
            fields = [int(header[index:index + 8], 16) for index in range(6, 110, 8)]
        except ValueError as error:
            raise ValueError("RPM cpio header contains an invalid integer") from error
        inode, mode, uid, gid, count, _, size, major, minor, _, _, length, check = fields
        if not 1 <= length <= 4096 or count < 1:
            raise ValueError("RPM cpio name or link count exceeds its bounds")
        raw = exact(stream, length)
        if not raw.endswith(b"\0") or b"\0" in raw.rstrip(b"\0"):
            raise ValueError("RPM cpio name is not terminated")
        path = raw.rstrip(b"\0").decode("utf-8")
        exact(stream, -(110 + length) % 4)
        if path == "TRAILER!!!":
            if size != 0:
                raise ValueError("RPM cpio trailer carries data")
            break
        if path in (".", "./") and stat.S_ISDIR(mode) and size == 0:
            continue
        path = name(path)
        entry = Entry(path, stat.S_IMODE(mode), uid, gid, "directory")
        if stat.S_ISREG(mode):
            digest, checksum = content(stream, size)
            entry = replace(entry, kind="file", size=size, sha256=digest)
            if count > 1:
                links.setdefault((major, minor, inode), []).append(path)
        elif stat.S_ISLNK(mode):
            if not 0 < size <= 4096:
                raise ValueError("RPM symbolic link exceeds its target bound")
            target = exact(stream, size)
            if b"\0" in target:
                raise ValueError("RPM symbolic link contains a null target")
            checksum = sum(target) & 0xFFFFFFFF
            entry = replace(entry, kind="symlink", target=target.decode("utf-8"))
        elif stat.S_ISDIR(mode) and size == 0:
            checksum = 0
        else:
            raise ValueError("RPM payload contains an unsupported file type")
        if (header[:6] == b"070702" and checksum != check
                or header[:6] == b"070701" and check != 0):
            raise ValueError("RPM cpio member checksum differs")
        exact(stream, -size % 4)
        add(entries, entry)
    for paths in links.values():
        bodies = {entries[path].sha256 for path in paths if entries[path].size}
        if len(bodies) > 1:
            raise ValueError("RPM hard links disagree about their contents")
        target = next((path for path in paths if entries[path].size), paths[0])
        for path in paths:
            if path != target:
                entries[path] = replace(entries[path], kind="hardlink", target=target)
    return entries


def inspect(package: Path, installation: str, expected_sha256: str) -> dict[str, Entry]:
    decoders = {"deb": ["dpkg-deb", "--fsys-tarfile"], "rpm": ["rpm2cpio"],
                "pacman": ["zstd", "--decompress", "--stdout", "--"]}
    if installation not in decoders:
        raise ValueError("native payload inspection requires deb, rpm or pacman")
    with tempfile.TemporaryDirectory(prefix="paintedwolf-final-package-") as directory:
        snapshot = Path(directory) / "package"
        with package.open("rb") as source, snapshot.open("xb") as destination:
            total, digest = 0, hashlib.sha256()
            while block := source.read(CHUNK):
                total += len(block)
                if total > MAX_BYTES:
                    raise ValueError("final package exceeds its byte bound")
                destination.write(block)
                digest.update(block)
        if digest.hexdigest() != expected_sha256:
            raise ValueError("final package differs from the selected artifact")
        with open(os.devnull, "wb") as errors:
            process = subprocess.Popen([*decoders[installation], str(snapshot)], stdout=subprocess.PIPE,
                                       stderr=errors, env={"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "LC_ALL": "C"})
            deadline = threading.Timer(300, process.kill)
            deadline.daemon = True
            deadline.start()
            try:
                stream = BoundedReader(process.stdout)
                entries = cpio_entries(stream) if installation == "rpm" else tar_entries(stream)
                while block := stream.read():
                    if installation == "rpm" and block.strip(b"\0"):
                        raise ValueError("RPM cpio payload carries trailing nonpadding bytes")
                if process.wait() != 0:
                    raise ValueError("native package decoder did not complete")
            finally:
                deadline.cancel()
                process.stdout.close()
                if process.poll() is None:
                    process.kill()
                process.wait()
    if not entries:
        raise ValueError("final package has an empty payload")
    return entries


def verify(entries: dict[str, Entry], installed: Path = Path("/")) -> dict:
    root = installed.resolve(strict=True)
    checked = 0
    for entry in entries.values():
        path = root / entry.name
        parent = path.parent.resolve(strict=True)
        if not parent.is_relative_to(root):
            raise ValueError("installed package parent escapes its selected filesystem")
        before = path.lstat()
        if (stat.S_IMODE(before.st_mode), before.st_uid, before.st_gid) != (entry.mode, entry.uid, entry.gid):
            raise MetadataMismatch(entry, before)
        if entry.kind == "directory":
            if not stat.S_ISDIR(before.st_mode):
                raise ValueError("installed package directory changed type")
            continue
        if entry.kind == "symlink":
            if not stat.S_ISLNK(before.st_mode) or os.readlink(path) != entry.target:
                raise ValueError("installed package symbolic link differs")
        else:
            if not stat.S_ISREG(before.st_mode):
                raise ValueError("installed package file changed type")
            with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), "rb") as source:
                opened = os.fstat(source.fileno())
                wanted = entries.get(entry.target) if entry.kind == "hardlink" else entry
                if stable(opened) != stable(before):
                    raise ValueError("installed package file changed before opening")
                if wanted is None:
                    raise ValueError("package hard link has no payload target")
                if wanted.kind != "file" or content(source, opened.st_size)[0] != wanted.sha256 or opened.st_size != wanted.size:
                    raise ValueError("installed package bytes differ from final payload")
                if stable(os.fstat(source.fileno())) != stable(opened):
                    raise ValueError("installed package file changed while reading")
            if entry.kind == "hardlink":
                other = (root / entry.target).stat()
                if (before.st_dev, before.st_ino) != (other.st_dev, other.st_ino):
                    raise ValueError("installed package hard link identity differs")
        if stable(path.lstat()) != stable(before):
            raise ValueError("installed package changed during verification")
        checked += 1
    return {"status": "passed", "payload_entries": len(entries), "files_and_links": checked}

"""Write a deterministic Linux initramfs without extracting guest paths onto the host."""

import gzip
import os
import stat
import tarfile
from dataclasses import dataclass
from pathlib import PurePosixPath


@dataclass(frozen=True)
class Entry:
    mode: int
    data: bytes = b""
    uid: int = 0
    gid: int = 0


def guest_path(name):
    path = PurePosixPath(name)
    if path.is_absolute() or ".." in path.parts or "\0" in name:
        raise ValueError(f"invalid guest path {name!r}")
    return str(path)


def collect_tar(tree, archive, select):
    hardlinks = []
    with tarfile.open(archive, "r:*") as source:
        for item in source:
            path = select(guest_path(item.name))
            if not path or path == ".":
                continue
            mode = 0o755 if item.isdir() or item.mode & 0o111 else 0o644
            if item.isdir():
                entry = Entry(stat.S_IFDIR | mode)
            elif item.issym():
                entry = Entry(stat.S_IFLNK | 0o777, os.fsencode(item.linkname))
            elif item.isfile():
                with source.extractfile(item) as data:
                    entry = Entry(stat.S_IFREG | mode, data.read())
            elif item.islnk():
                hardlinks.append((path, select(guest_path(item.linkname))))
                continue
            else:
                raise ValueError(f"unsupported guest entry {item.name}")
            tree[path] = entry
    for path, target in hardlinks:
        tree[path] = tree[target]


def pack(tree, output):
    for name in sorted(tree):
        if name != guest_path(name):
            raise ValueError(f"invalid archive entry {name}")
        for parent in PurePosixPath(name).parents:
            if str(parent) != ".":
                tree.setdefault(str(parent), Entry(stat.S_IFDIR | 0o755))
    with (
        output.open("wb") as raw,
        gzip.GzipFile(
            filename="", fileobj=raw, mode="wb", mtime=0, compresslevel=6
        ) as dest,
    ):
        for ino, (name, entry) in enumerate(
            [*sorted(tree.items()), ("TRAILER!!!", Entry(0))], 1
        ):
            for parent in PurePosixPath(name).parents:
                if str(parent) != "." and not stat.S_ISDIR(tree[str(parent)].mode):
                    raise ValueError(f"non-directory parent of guest entry {name}")
            label = os.fsencode(name) + b"\0"
            fields = (
                ino,
                entry.mode,
                entry.uid,
                entry.gid,
                1,
                0,
                len(entry.data),
                0,
                0,
                0,
                0,
                len(label),
                0,
            )
            header = b"070701" + b"".join(
                f"{value:08x}".encode("ascii") for value in fields
            )
            dest.write(header + label + bytes(-(len(header) + len(label)) % 4))
            dest.write(entry.data + bytes(-len(entry.data) % 4))

"""Seal app-managed files, directories, links, resources and libraries together."""
import hashlib
import os
import posixpath
from pathlib import Path, PurePosixPath
import stat


def record(name: str, source: Path) -> dict:
    before = source.lstat()
    row = {"path": name, "kind": None, "mode": stat.S_IMODE(before.st_mode),
           "sha256": None, "size": None, "target": None}
    if stat.S_ISLNK(before.st_mode):
        row.update(kind="symlink", mode=0o777, target=os.readlink(source))
    elif stat.S_ISDIR(before.st_mode):
        row["kind"] = "directory"
    elif stat.S_ISREG(before.st_mode):
        hasher, size = hashlib.sha256(), 0
        with source.open("rb") as handle:
            while chunk := handle.read(1 << 20):
                hasher.update(chunk)
                size += len(chunk)
        after = source.lstat()
        fields = ("st_dev", "st_ino", "st_size", "st_mtime_ns", "st_ctime_ns")
        if any(getattr(before, key) != getattr(after, key) for key in fields):
            raise ValueError("payload changed while sealing " + name)
        row.update(kind="file", sha256=hasher.hexdigest(), size=size)
    else:
        raise ValueError("payload contains an unsupported object: " + name)
    return row


def collect(sources: dict[str, Path]) -> list[dict]:
    rows = {}
    for name, source in sources.items():
        path = PurePosixPath(name)
        if path.is_absolute() or str(path) != name or ".." in path.parts or name in {".", "manifest.json"}:
            raise ValueError("invalid payload inventory source")
        pending = [(name, source)]
        while pending:
            relative, physical = pending.pop()
            row = record(relative, physical)
            if relative in rows and rows[relative] != row:
                raise ValueError("conflicting payload source: " + relative)
            rows[relative] = row
            if row["kind"] == "directory":
                pending.extend((relative + "/" + child.name, child) for child in physical.iterdir())
    for name in list(rows):
        for parent in PurePosixPath(name).parents:
            if str(parent) != ".":
                rows.setdefault(str(parent), {"path": str(parent), "kind": "directory", "mode": 0o755,
                                              "sha256": None, "size": None, "target": None})
    for name, row in rows.items():
        for parent in PurePosixPath(name).parents:
            if str(parent) != "." and rows[str(parent)]["kind"] != "directory":
                raise ValueError("payload parent is not a physical directory")
        if row["kind"] != "symlink":
            continue
        selected, visited = name, set()
        while rows[selected]["kind"] == "symlink":
            if selected in visited:
                raise ValueError("cyclic payload symlink")
            visited.add(selected)
            target = rows[selected]["target"]
            if not target or target.startswith("/"):
                raise ValueError("absolute or empty payload symlink")
            selected = posixpath.normpath(posixpath.join(posixpath.dirname(selected), target))
            if selected == ".":
                break
            if selected not in rows:
                raise ValueError("escaping or dangling payload symlink")
    return [rows[name] for name in sorted(rows)]


def tree(root: Path) -> list[dict]:
    if root.is_symlink() or not root.is_dir():
        raise ValueError("payload root must be a physical directory")
    return collect({child.name: child for child in root.iterdir() if child.name != "manifest.json"})

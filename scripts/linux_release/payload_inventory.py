"""Seal app-managed files, directories, links, resources and libraries together."""
import hashlib
import os
import posixpath
from pathlib import Path, PurePosixPath
import re
import stat

FORMAT_VERSION = 2
HEX_DIGEST = re.compile(r"^[0-9a-f]{64}$")
PAYLOAD_KEYS = {"path", "kind", "mode", "sha256", "size", "target"}


def record(name: str, source: Path, canonical_modes: bool) -> dict:
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
    if row["kind"] != "symlink":
        if canonical_modes:
            row["mode"] &= ~0o022
        if row["mode"] & 0o7022:
            raise ValueError("unsafe payload mode: " + name)
    return row


def collect(sources: dict[str, Path], *, canonical_modes=False) -> list[dict]:
    rows = {}
    for name, source in sources.items():
        path = PurePosixPath(name)
        if path.is_absolute() or str(path) != name or ".." in path.parts or name in {".", "manifest.json"}:
            raise ValueError("invalid payload inventory source")
        pending = [(name, source)]
        while pending:
            relative, physical = pending.pop()
            row = record(relative, physical, canonical_modes)
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


def validate_payload(payload: object, executables: list[dict], install_root: str) -> None:
    if not isinstance(payload, list) or not payload:
        raise ValueError("manifest payload must be a non-empty list")
    entries: dict[str, dict] = {}
    for row in payload:
        if not isinstance(row, dict) or set(row) != PAYLOAD_KEYS:
            raise ValueError(f"payload entry has unexpected keys: {row}")
        name, kind, mode = row["path"], row["kind"], row["mode"]
        if (not isinstance(name, str) or not name or name in {".", "manifest.json"}
                or PurePosixPath(name).is_absolute() or PurePosixPath(name).as_posix() != name
                or ".." in PurePosixPath(name).parts or name in entries):
            raise ValueError(f"invalid or duplicate payload path: {name!r}")
        if type(mode) is not int or mode < 0 or mode > 0o777 or (kind != "symlink" and mode & 0o022):
            raise ValueError(f"unsafe payload mode for {name}")
        sha, size, target = row["sha256"], row["size"], row["target"]
        if kind == "file":
            if (not isinstance(sha, str) or not HEX_DIGEST.match(sha)
                    or type(size) is not int or size < 0 or target is not None):
                raise ValueError(f"invalid file payload record for {name}")
        elif kind == "directory":
            if mode & 0o500 != 0o500 or sha is not None or size is not None or target is not None:
                raise ValueError(f"invalid directory payload record for {name}")
        elif kind == "symlink":
            if (mode != 0o777 or sha is not None or size is not None
                    or not isinstance(target, str) or not target or target.startswith("/")):
                raise ValueError(f"invalid symlink payload record for {name}")
        else:
            raise ValueError(f"unknown payload kind for {name}: {kind!r}")
        entries[name] = row
    for name, row in entries.items():
        for parent in PurePosixPath(name).parents:
            if str(parent) != "." and entries.get(str(parent), {}).get("kind") != "directory":
                raise ValueError(f"payload parent is not a physical directory: {parent}")
        if row["kind"] != "symlink":
            continue
        selected, visited = name, set()
        while entries[selected]["kind"] == "symlink":
            if selected in visited:
                raise ValueError(f"cyclic payload symlink: {name}")
            visited.add(selected)
            selected = posixpath.normpath(posixpath.join(posixpath.dirname(selected), entries[selected]["target"]))
            if selected == ".":
                break
            if selected not in entries:
                raise ValueError(f"escaping or dangling payload symlink: {name}")
    prefix = install_root.rstrip("/") + "/"
    for exe in executables:
        if not exe["path"].startswith(prefix):
            continue
        rel = exe["path"][len(prefix):]
        item = entries.get(rel)
        if (item is None or item["kind"] != "file" or not (item["mode"] & 0o111)
                or item["sha256"] != exe["sha256"] or item["size"] != exe["size"]):
            raise ValueError(f"executable differs from payload inventory: {exe['path']}")

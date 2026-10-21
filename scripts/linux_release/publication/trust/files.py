"""Bind installed file-artifact identities to the bytes selected for publication."""

import hashlib
import os
from pathlib import Path
import stat

from ...installed_evidence import artifacts


def verify_files(document: object, source_commit: str, root: Path) -> list[dict]:
    root = root.resolve(strict=True)
    if not root.is_dir():
        raise ValueError("publication file root must be a directory")
    verified = []
    for name, artifact in sorted(artifacts(document, source_commit).items()):
        identity = artifact["identity"]
        if identity["kind"] != "file":
            continue
        path = root / name
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(descriptor, "rb") as stream:
            before = os.fstat(stream.fileno())
            if not stat.S_ISREG(before.st_mode):
                raise ValueError("publication artifact must be a regular file")
            digest = hashlib.sha256()
            for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(chunk)
            after = os.fstat(stream.fileno())
            current = path.stat(follow_symlinks=False)
        snapshot = lambda value: (value.st_dev, value.st_ino, value.st_size,
                                  value.st_mtime_ns, value.st_ctime_ns, value.st_mode)
        if snapshot(before) != snapshot(after) or snapshot(after) != snapshot(current):
            raise ValueError("publication artifact changed while verifying")
        if digest.hexdigest() != identity["sha256"]:
            raise ValueError(f"publication bytes differ from installed artifact {name}")
        verified.append({"id": name, "sha256": digest.hexdigest(), "bytes": after.st_size})
    return verified

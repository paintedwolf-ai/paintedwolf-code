"""Canonical inventory identities and atomic immutable repository object storage."""

import json
import os
from pathlib import Path
import tempfile


def encoded(value):
    return (json.dumps(value, sort_keys=True, indent=2) + "\n").encode()


def immutable(path: Path, data: bytes):
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as staging:
        staging.write(data)
        staging.flush()
        os.fsync(staging.fileno())
        staged = Path(staging.name)
    try:
        try:
            os.link(staged, path)
        except FileExistsError:
            if path.is_symlink() or path.read_bytes() != data:
                raise ValueError("immutable repository object collision")
    finally:
        staged.unlink()



def replace(path: Path, data: bytes):
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as staging:
        staging.write(data)
        staging.flush()
        os.fsync(staging.fileno())
        staged = Path(staging.name)
    os.replace(staged, path)



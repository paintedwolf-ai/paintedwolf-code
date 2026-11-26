import hashlib
from pathlib import Path


def hash_tree(root: Path, rel_paths: list[str], chunk_size: int = 16) -> str:
    # Old unbounded implementation to be replaced by edit_program.py.
    h = hashlib.sha256()
    for rel in sorted(rel_paths):
        h.update(rel.encode("utf-8"))
        h.update(b"\x00")
        with open(root / rel, "rb") as fh:
            h.update(fh.read())
    return h.hexdigest()

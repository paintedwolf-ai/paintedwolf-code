"""JSON state files shared by the verification queue."""

import json
import os
from pathlib import Path


def write_json(path, value):
    path = Path(path)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    temporary.replace(path)


def read_json(path):
    return json.loads(Path(path).read_text())


def read_optional(path):
    try:
        return read_json(path)
    except FileNotFoundError:
        return None


def read_state(path):
    """Missing, unreadable, or malformed state reads as empty."""
    try:
        value = read_json(path)
    except (OSError, ValueError):
        return {}
    return value if isinstance(value, dict) else {}

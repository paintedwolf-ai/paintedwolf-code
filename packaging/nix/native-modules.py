"""Select native glibc ELF dependencies from installed npm package metadata."""

import json
from pathlib import Path
import struct
import sys


def matches(value, selected):
    if value is None:
        return True
    values = [value] if isinstance(value, str) else value
    return selected not in [item[1:] for item in values if item.startswith("!")] and (
        not any(not item.startswith("!") for item in values) or selected in values)


def select(root, architecture):
    cpu, machine = {"x86_64": ("x64", 62), "aarch64": ("arm64", 183)}[architecture]
    result = []
    for path in sorted(root.rglob("*")):
        if path.is_symlink() or not path.is_file():
            continue
        with path.open("rb") as source:
            header = source.read(20)
        if header[:4] != b"\x7fELF":
            continue
        if len(header) != 20 or header[4:6] != bytes([2, 1]):
            continue
        if struct.unpack_from("<H", header, 18)[0] != machine:
            continue
        owner = path.parent
        while not (owner / "package.json").is_file() and owner != root:
            owner = owner.parent
        metadata = json.loads((owner / "package.json").read_text())
        if all(matches(metadata.get(field), value) for field, value in
               [("os", "linux"), ("cpu", cpu), ("libc", "glibc")]):
            result.append(path)
    return result


if __name__ == "__main__":
    for native in select(Path(sys.argv[1]).resolve(), sys.argv[2]):
        sys.stdout.buffer.write(str(native).encode() + b"\0")

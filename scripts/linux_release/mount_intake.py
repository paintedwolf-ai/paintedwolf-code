"""Build and inspect the fixed, pre-runtime Linux intake transition."""

import argparse
from pathlib import Path
import shutil
import struct
import subprocess

ROOT = Path(__file__).resolve().parents[2]
MACHINES = {"amd64": 62, "arm64": 183}


def inspect(raw, architecture):
    """Require a bounded static native entry without dynamic startup code."""
    if (not 64 <= len(raw) <= 1 << 20
            or raw[:7] != b"\x7fELF\x02\x01\x01"
            or struct.unpack_from("<HH", raw, 16) != (2, MACHINES[architecture])):
        raise ValueError("intake transition is not the selected native static ELF")
    entry, phoff, shoff = struct.unpack_from("<QQQ", raw, 24)
    ehsize, phsize, phnum, shsize, shnum, shnames = struct.unpack_from("<6H", raw, 52)
    if (ehsize != 64 or phsize != 56 or not 0 < phnum <= 128
            or phoff + phsize * phnum > len(raw)
            or shsize != 64 or not 0 < shnum <= 1024 or shnames >= shnum
            or shoff + shsize * shnum > len(raw)):
        raise ValueError("intake transition has invalid ELF metadata")
    executable_entry = False
    stack = False
    for index in range(phnum):
        kind, flags, offset, address, _, size, memory, _ = struct.unpack_from(
            "<II6Q", raw, phoff + index * phsize)
        if offset + size > len(raw) or size > memory:
            raise ValueError("intake transition has invalid segment bounds")
        if kind in (2, 3, 7):  # DYNAMIC, INTERP, TLS
            raise ValueError("intake transition requires a runtime or loader")
        if kind == 1 and flags & 3 == 3:
            raise ValueError("intake transition has writable executable code")
        if kind == 1 and flags & 1 and address <= entry < address + size:
            executable_entry = True
        if kind == 0x6474E551:
            stack = True
            if flags & 1:
                raise ValueError("intake transition requires an executable stack")
    if not executable_entry or not stack:
        raise ValueError("intake transition has no bounded executable entry/stack")
    name_row = struct.unpack_from("<II4QII2Q", raw, shoff + shnames * shsize)
    name_offset, name_size = name_row[4], name_row[5]
    if name_offset + name_size > len(raw):
        raise ValueError("intake transition section names are truncated")
    names = raw[name_offset:name_offset + name_size]
    for index in range(shnum):
        name, kind, _, _, offset, size, *_ = struct.unpack_from(
            "<II4QII2Q", raw, shoff + index * shsize)
        if kind != 8 and offset + size > len(raw):
            raise ValueError("intake transition section is truncated")
        if name >= len(names) or b"\0" not in names[name:]:
            raise ValueError("intake transition section name is invalid")
        label = names[name:].split(b"\0", 1)[0]
        if kind in (6, 14, 15, 16) or label in (b".init", b".fini"):
            raise ValueError("intake transition has dynamic initialization")


def build(output, architecture):
    compiler = shutil.which("cc")
    if compiler is None:
        raise ValueError("a native C compiler is required for the intake transition")
    sources = ROOT / "packaging/linux/mount-intake"
    output = Path(output)
    output.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run([
        compiler, "-O2", "-std=c11", "-ffreestanding", "-fno-builtin",
        "-fno-stack-protector", "-fno-pie", "-fno-unwind-tables",
        "-fno-asynchronous-unwind-tables", "-fno-tree-vectorize",
        "-nostdlib", "-static", "-no-pie", "-Wl,--no-undefined",
        "-Wl,-z,noexecstack", "-Wl,-z,relro,-z,now", "-Wl,--build-id=none",
        f"-ffile-prefix-map={ROOT}=.", "-s", "-I", str(sources),
        str(sources / "transition.c"), str(sources / "entry.S"),
        "-o", str(output),
    ], check=True)
    inspect(output.read_bytes(), architecture)
    output.chmod(0o755)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--architecture", choices=tuple(MACHINES), required=True)
    arguments = parser.parse_args()
    build(arguments.output, arguments.architecture)


if __name__ == "__main__":
    main()

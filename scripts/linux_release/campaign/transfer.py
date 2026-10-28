"""Transfer only bounded regular installed-campaign files across guest boundaries."""
from __future__ import annotations

import argparse
import os
from pathlib import Path, PurePosixPath
import stat
import sys
import zipfile

MAX_BYTES = 1 << 30
MAX_FILE = 32 << 20
MAX_FILES = 20_000


def relative(value: str) -> str:
    path = PurePosixPath(value)
    if not value or path.is_absolute() or str(path) != value or ".." in path.parts or "\0" in value:
        raise ValueError("campaign transfer contains an invalid relative path")
    return value


def export(root: Path, output):
    root = root.resolve(strict=True)
    if not all((root / name).is_file() for name in ("campaign.json", "binding.json", "driver-registry.json", "execution.json", "variant.json")):
        raise ValueError("campaign transfer requires an executed leaf output")
    total, count = 0, 0
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED, allowZip64=True) as archive:
        for path in sorted(root.rglob("*")):
            info = path.lstat()
            if stat.S_ISDIR(info.st_mode):
                continue
            if not stat.S_ISREG(info.st_mode) or info.st_size > MAX_FILE:
                raise ValueError("campaign transfer requires bounded regular files")
            total, count = total + info.st_size, count + 1
            if total > MAX_BYTES or count > MAX_FILES:
                raise ValueError("campaign transfer exceeds its size or file bound")
            name = relative(path.relative_to(root).as_posix())
            descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            with os.fdopen(descriptor, "rb") as stream:
                before = os.fstat(stream.fileno())
                if not stat.S_ISREG(before.st_mode) or (before.st_dev, before.st_ino, before.st_size) != (info.st_dev, info.st_ino, info.st_size):
                    raise ValueError("campaign file identity changed before transfer")
                with archive.open(name, "w", force_zip64=True) as target:
                    copied = 0
                    while block := stream.read(1 << 20):
                        copied += len(block)
                        if copied > before.st_size:
                            raise ValueError("campaign file grew during transfer")
                        target.write(block)
                after = os.fstat(stream.fileno())
                if (copied != before.st_size or (before.st_dev, before.st_ino, before.st_mtime_ns, before.st_ctime_ns)
                        != (after.st_dev, after.st_ino, after.st_mtime_ns, after.st_ctime_ns)):
                    raise ValueError("campaign file changed during transfer")


def receive(source: Path, output: Path):
    if source.stat().st_size > MAX_BYTES:
        raise ValueError("compressed campaign transfer exceeds its bound")
    output.mkdir(parents=True, mode=0o700, exist_ok=False)
    total, seen = 0, set()
    with zipfile.ZipFile(source) as archive:
        entries = archive.infolist()
        if len(entries) > MAX_FILES:
            raise ValueError("campaign transfer exceeds its file bound")
        for item in entries:
            name = relative(item.filename)
            mode = item.external_attr >> 16
            if (name in seen or item.is_dir() or stat.S_IFMT(mode) not in {0, stat.S_IFREG}
                    or not 0 <= item.file_size <= MAX_FILE):
                raise ValueError("campaign transfer repeats a path or contains an unsupported file")
            seen.add(name)
            total += item.file_size
            if total > MAX_BYTES:
                raise ValueError("campaign transfer exceeds its expanded byte bound")
            path = output / name
            path.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
            with archive.open(item) as stream, path.open("xb") as target:
                copied = 0
                while block := stream.read(1 << 20):
                    copied += len(block)
                    if copied > item.file_size:
                        raise ValueError("campaign transfer entry exceeds its declared size")
                    target.write(block)
            if copied != item.file_size:
                raise ValueError("campaign transfer entry is truncated")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--export", type=Path, required=True)
    args = parser.parse_args()
    try:
        export(args.export, sys.stdout.buffer)
    except (OSError, ValueError, zipfile.BadZipFile) as error:
        print("Campaign transfer refused: " + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

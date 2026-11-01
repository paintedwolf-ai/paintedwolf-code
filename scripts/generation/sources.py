"""Fetch digest-pinned runtime source trees into an isolated tool cache."""

import fcntl
import hashlib
from pathlib import Path, PurePosixPath
import shutil
import tarfile
import tempfile
import urllib.request


def unpack(archive, destination):
    with tarfile.open(archive) as bundle:
        members = bundle.getmembers()
        if len(members) > 100000 or sum(m.size for m in members) > 1 << 30:
            raise ValueError("runtime source archive exceeds its bounds")
        roots = set()
        for member in members:
            name = PurePosixPath(member.name)
            if (name.is_absolute() or ".." in name.parts or not name.parts
                    or not (member.isfile() or member.isdir())):
                raise ValueError("runtime source archive contains an unsafe entry")
            roots.add(name.parts[0])
        if len(roots) != 1:
            raise ValueError("runtime source archive must have one root")
        for member in members:
            relative = Path(*PurePosixPath(member.name).parts[1:])
            target = destination / relative
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                with bundle.extractfile(member) as source, target.open("xb") as output:
                    shutil.copyfileobj(source, output, 1 << 20)
                target.chmod(0o755 if member.mode & 0o111 else 0o644)


def ensure_source(pin, cache):
    cache.mkdir(parents=True, exist_ok=True)
    destination = cache / pin["sha256"]
    with (cache / (pin["sha256"] + ".lock")).open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if (destination / ".source-sha256").is_file():
            if (destination / ".source-sha256").read_text().strip() != pin["sha256"]:
                raise ValueError("runtime source cache identity mismatch")
            return destination
        with tempfile.TemporaryDirectory(prefix=".source-", dir=cache) as temporary:
            temporary = Path(temporary)
            archive = temporary / "source.tar.gz"
            digest, size = hashlib.sha256(), 0
            with urllib.request.urlopen(pin["url"], timeout=60) as source, archive.open("xb") as output:
                for block in iter(lambda: source.read(1 << 20), b""):
                    size += len(block)
                    if size > 128 << 20:
                        raise ValueError("runtime source download exceeds its bound")
                    digest.update(block)
                    output.write(block)
            if digest.hexdigest() != pin["sha256"]:
                raise ValueError("runtime source digest mismatch")
            tree = temporary / "tree"
            tree.mkdir()
            unpack(archive, tree)
            (tree / ".source-sha256").write_text(pin["sha256"] + "\n")
            tree.rename(destination)
    return destination

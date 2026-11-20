"""Build the portable service commands against the guest's musl runtime."""

import hashlib
import os
from pathlib import Path
import re
import stat
import subprocess
import tarfile
import tempfile

from initramfs import Entry, collect_tar


def base_userland(archive, machine):
    tree = {}
    loader = f"lib/ld-musl-{machine}.so.1"
    with tarfile.open(archive) as upstream:
        for m in upstream:
            name = m.name.removeprefix("./")
            if m.issym() and os.path.basename(m.linkname) == "busybox":
                tree[name] = Entry(stat.S_IFLNK | 0o777, b"/bin/busybox")
    collect_tar(tree, archive, lambda name: name if name in {"bin/busybox", loader, f"lib/libc.musl-{machine}.so.1", "etc/passwd", "etc/group"} else None)
    if not tree[loader].data.startswith(b"\x7fELF") or not tree["bin/sh"].data or not re.search(rb"^[^:]+:[^:]*:65534:65534:", tree["etc/passwd"].data, re.M):
        raise ValueError("incomplete guest shell, C runtime or service identity")
    return tree


def extract_sources(archive, directory):
    with tarfile.open(archive) as source:
        source.extractall(directory, filter="data")
    roots = list(directory.iterdir())
    if len(roots) != 1 or not roots[0].is_dir():
        raise ValueError("unexpected upstream source tree")
    return roots[0]


def build_postgres(source, compiler, machine, libc, cache):
    recipe = hashlib.sha256(Path(__file__).read_bytes() + libc.data).hexdigest()
    cached = cache / f"postgres-{source.name}-{compiler.name}-{machine}-{recipe}.tar"
    if cached.exists():
        return cached
    with tempfile.TemporaryDirectory(dir=cache) as scratch:
        work = Path(scratch)
        for name in ("source", "compiler", "bin", "libc", "build"):
            (work / name).mkdir()
        src = extract_sources(source, work / "source")
        zig = extract_sources(compiler, work / "compiler") / "zig"
        (work / "libc/libc.so").write_bytes(libc.data)
        for driver, args in {"cc": 'cc -target "$PW_GUEST_TARGET"', "ar": "ar", "ranlib": "ranlib"}.items():
            wrapper = work / "bin" / f"pw-guest-{driver}"
            wrapper.write_text(f'#!/bin/sh\nexec "$PW_GUEST_ZIG" {args} "$@"\n')
            wrapper.chmod(0o755)
        env = os.environ | {"PATH": str(work / "bin") + os.pathsep + os.environ["PATH"],
                            "PW_GUEST_ZIG": str(zig), "PW_GUEST_TARGET": machine+"-linux-musl",
                            "CC": "pw-guest-cc", "AR": "pw-guest-ar", "RANLIB": "pw-guest-ranlib",
                            "CFLAGS": f"-O2 -g0 -fPIC -ffile-prefix-map={work}=/build",
                            "LDFLAGS_EX": f"-dynamic -L{work}/libc", "LDFLAGS_EX_BE": f"-dynamic -L{work}/libc",
                            "SOURCE_DATE_EPOCH": "0", "LC_ALL": "C", "TZ": "UTC"}
        build = work / "build"
        with (cache / f"postgres-build-{machine}.log").open("wb") as log:
            def run(args):
                try:
                    subprocess.run(args, cwd=build, env=env, stdout=log, stderr=subprocess.STDOUT, check=True)
                except subprocess.CalledProcessError as exc:
                    raise RuntimeError(f"guest PostgreSQL build failed; see {log.name}") from exc
            run([str(src / "configure"), "--prefix=/usr", "--host="+machine+"-linux-musl", "--without-readline", "--without-zlib", "--without-icu"])
            run(["make", "-j4", "-C", "src/backend", "generated-headers"])
            run(["make", "-j4"])
            run(["make", "install", "DESTDIR="+str(work / "install")])
        artifact = work / "postgres.tar"
        with tarfile.open(artifact, "w") as dest:
            dest.add(work / "install/usr", arcname="usr")
        os.replace(artifact, cached)
    return cached


def add_postgres(tree, archive):
    commands = {"initdb", "postgres", "psql", "pg_isready", "pg_ctl", "createdb", "createuser", "pg_dump", "pg_restore"}
    def select(name):
        parts = name.split("/")
        if len(parts) == 3 and parts[:2] == ["usr", "bin"] and parts[2] in commands:
            return name
        if name.startswith("usr/share/postgresql/"):
            return name
        if name.startswith("usr/lib/libpq.so") or (name.startswith("usr/lib/postgresql/") and name.endswith(".so")):
            return name
        return None
    collect_tar(tree, archive, select)
    if any(f"usr/bin/{cmd}" not in tree for cmd in commands):
        raise ValueError("guest PostgreSQL tools missing")

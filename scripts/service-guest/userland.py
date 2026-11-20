"""Build the portable service commands against the guest's musl runtime."""

import os
import re
import stat
import tarfile

from initramfs import Entry, collect_tar


def base_userland(archive, machine):
    tree = {}
    loader = f"lib/ld-musl-{machine}.so.1"
    with tarfile.open(archive) as upstream:
        for m in upstream:
            name = m.name.removeprefix("./")
            if m.issym() and os.path.basename(m.linkname) == "busybox":
                tree[name] = Entry(
                    stat.S_IFLNK | 0o777,
                    os.fsencode(os.path.relpath("bin/busybox", os.path.dirname(name))),
                )
    collect_tar(
        tree,
        archive,
        lambda name: (
            name
            if name
            in {
                "bin/busybox",
                loader,
                f"lib/libc.musl-{machine}.so.1",
                "etc/passwd",
                "etc/group",
            }
            else None
        ),
    )
    if (
        not tree[loader].data.startswith(b"\x7fELF")
        or not tree["bin/sh"].data
        or not re.search(rb"^[^:]+:[^:]*:65534:65534:", tree["etc/passwd"].data, re.M)
    ):
        raise ValueError("incomplete guest shell, C runtime or service identity")
    return tree


def add_postgres(tree, archive):
    commands = {
        "initdb",
        "postgres",
        "psql",
        "pg_isready",
        "pg_ctl",
        "createdb",
        "createuser",
        "pg_dump",
        "pg_restore",
    }

    def select(name):
        parts = name.split("/")
        if len(parts) == 3 and parts[:2] == ["usr", "bin"] and parts[2] in commands:
            return name
        if name.startswith("usr/share/postgresql/"):
            return name
        if name.startswith("usr/lib/libpq.so") or (
            name.startswith("usr/lib/postgresql/") and name.endswith(".so")
        ):
            return name
        return None

    collect_tar(tree, archive, select)
    if any(f"usr/bin/{cmd}" not in tree for cmd in commands):
        raise ValueError("guest PostgreSQL tools missing")

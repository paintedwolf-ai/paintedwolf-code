"""Cross-compile the pinned PostgreSQL sources for the guest."""

import hashlib
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile


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
        for name in ("source", "compiler", "bin", "libc", "build", "native"):
            (work / name).mkdir()
        src = extract_sources(source, work / "source")
        zig = extract_sources(compiler, work / "compiler") / "zig"
        (work / "libc/libc.so").write_bytes(libc.data)
        for driver, args in {
            "cc": 'cc -target "$PW_GUEST_TARGET" -Wno-unused-command-line-argument -dynamic "-L$PW_GUEST_WORK/libc" "-ffile-prefix-map=$PW_GUEST_WORK=/build"',
            "ar": "ar",
            "ranlib": "ranlib",
        }.items():
            wrapper = work / "bin" / f"pw-guest-{driver}"
            wrapper.write_text(f'#!/bin/sh\nexec "$PW_GUEST_ZIG" {args} "$@"\n')
            wrapper.chmod(0o755)
        env = os.environ | {
            "PATH": str(work / "bin") + os.pathsep + os.environ["PATH"],
            "PW_GUEST_ZIG": str(zig),
            "PW_GUEST_TARGET": machine + "-linux-musl",
            "PW_GUEST_WORK": str(work),
            "CC": "pw-guest-cc",
            "AR": "pw-guest-ar",
            "RANLIB": "pw-guest-ranlib",
            "CFLAGS": "-O2 -g0 -fPIC",
            "LDFLAGS_EX_BE": "-Wl,--export-dynamic",
            "SOURCE_DATE_EPOCH": "0",
            "LC_ALL": "C",
            "TZ": "UTC",
        }
        build = work / "build"
        with (cache / f"postgres-build-{machine}.log").open("wb") as log:

            def run(args, cwd=build, environment=env):
                try:
                    subprocess.run(
                        args,
                        cwd=cwd,
                        env=environment,
                        stdout=log,
                        stderr=subprocess.STDOUT,
                        check=True,
                    )
                except subprocess.CalledProcessError as exc:
                    raise RuntimeError(
                        f"guest PostgreSQL build failed; see {log.name}"
                    ) from exc

            native = work / "native"
            native_env = os.environ | {
                "LC_ALL": "C",
                "TZ": "UTC",
                "SOURCE_DATE_EPOCH": "0",
            }
            configure_flags = ["--without-readline", "--without-zlib", "--without-icu"]
            run([str(src / "configure"), *configure_flags], native, native_env)
            run(
                ["make", "-j4", "-C", "src/backend", "generated-headers"],
                native,
                native_env,
            )
            run(["make", "-j4", "-C", "src/timezone", "zic"], native, native_env)
            run(
                [
                    str(src / "configure"),
                    "--prefix=/usr",
                    "--host=" + machine + "-linux-musl",
                    "--without-readline",
                    "--without-zlib",
                    "--without-icu",
                ]
            )
            run(["make", "-j4", "-C", "src/backend", "generated-headers"])
            run(["make", "-j4"])
            run(
                [
                    "make",
                    "install",
                    "ZIC=" + str(native / "src/timezone/zic"),
                    "DESTDIR=" + str(work / "install"),
                ]
            )
        artifact = work / "postgres.tar"
        with tarfile.open(artifact, "w") as dest:
            dest.add(work / "install/usr", arcname="usr")
        os.replace(artifact, cached)
    return cached

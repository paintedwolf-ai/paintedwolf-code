import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import stat
import sys
import tarfile
import tempfile
import unittest
from types import SimpleNamespace
from contextlib import ExitStack
import subprocess
from unittest.mock import patch

with patch.object(
    sys, "path", [str(Path(__file__).parent / "service-guest"), *sys.path]
):
    import assemble
    import postgres
    from initramfs import collect_tar, Entry, pack
    from userland import base_userland, add_postgres


def archive(path, entries):
    with tarfile.open(path, "w:gz") as out:
        for name, kind, data in entries:
            row = tarfile.TarInfo(name)
            row.type = kind
            row.mode = 0o755
            if kind == tarfile.REGTYPE:
                row.size = len(data)
                out.addfile(row, io.BytesIO(data))
            else:
                row.linkname = data
                out.addfile(row)


class ServiceGuestStaging(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.dir = Path(temp.name).resolve()

    def test_pinned_download_is_verified_on_every_use(self):
        upstream = self.dir / "upstream"
        upstream.write_bytes(b"pinned guest bytes")
        digest = hashlib.sha256(upstream.read_bytes()).hexdigest()
        pin = {"sha256": digest, "url": upstream.as_uri()}
        cached = assemble.acquire(pin, self.dir)
        cached.write_bytes(b"damaged cache")
        self.assertEqual(
            assemble.acquire(pin, self.dir).read_bytes(), upstream.read_bytes()
        )
        upstream.unlink()
        self.assertEqual(assemble.acquire(pin, self.dir), cached)
        upstream.write_bytes(b"other")
        cached.unlink()
        with self.assertRaisesRegex(ValueError, "digest mismatch"):
            assemble.acquire(pin, self.dir)
        self.assertFalse(cached.exists())

    def test_shell_loader_and_links_remain_inside_guest_root(self):
        src = self.dir / "base.tar"
        archive(
            src,
            [
                ("bin/busybox", tarfile.REGTYPE, b"binary"),
                ("bin/sh", tarfile.SYMTYPE, "/bin/busybox"),
                ("usr/bin/printf", tarfile.SYMTYPE, "/bin/busybox"),
                ("lib/ld-musl-aarch64.so.1", tarfile.REGTYPE, b"\x7fELFmusl"),
                ("lib/libc.musl-aarch64.so.1", tarfile.SYMTYPE, "ld-musl-aarch64.so.1"),
                (
                    "etc/passwd",
                    tarfile.REGTYPE,
                    b"nobody:x:65534:65534:nobody:/:/sbin/nologin\n",
                ),
                ("sbin/apk", tarfile.REGTYPE, b"unshipped package manager"),
            ],
        )
        tree = base_userland(src, "aarch64")
        self.assertNotIn("sbin/apk", tree)
        one, two = self.dir / "one", self.dir / "two"
        pack(tree, one)
        pack(dict(reversed(list(tree.items()))), two)
        self.assertEqual(one.read_bytes(), two.read_bytes())
        self.assertTrue(gzip.decompress(one.read_bytes()).startswith(b"070701"))
        root = self.dir / "rootfs"
        assemble.stage_rootfs(tree, root)
        self.assertEqual((root / "usr/bin/printf").resolve(), root / "bin/busybox")
        self.assertEqual(
            (root / "lib/libc.musl-aarch64.so.1").read_bytes(), b"\x7fELFmusl"
        )
        self.assertEqual((root / "bin/busybox").stat().st_mode & 0o222, 0)

    def test_guest_preparation_refuses_unsafe_parents_and_incomplete_inputs(self):
        src = self.dir / "base.tar"
        archive(src, [("../outside", tarfile.REGTYPE, b"no")])
        with self.assertRaisesRegex(ValueError, "invalid guest path"):
            collect_tar({}, src, lambda x: x)
        tree = {
            "link": Entry(stat.S_IFLNK | 0o777, b"/outside"),
            "link/file": Entry(stat.S_IFREG | 0o444, b"no"),
        }
        with self.assertRaisesRegex(ValueError, "non-directory parent"):
            pack(tree, self.dir / "bad")
        archive(src, [("usr/bin/postgres", tarfile.REGTYPE, b"partial")])
        with self.assertRaisesRegex(ValueError, "PostgreSQL tools missing"):
            add_postgres({}, src)

    def test_postgres_cross_build_uses_native_zone_compiler_and_caches_result(self):
        src = self.dir / "source"
        compiler = self.dir / "toolchain"
        archive(src, [("postgres/configure", tarfile.REGTYPE, b"")])
        archive(compiler, [("compiler/zig", tarfile.REGTYPE, b"")])
        calls = []

        def build(command, **kwargs):
            calls.append(command)
            if command[0:2] == ["make", "install"]:
                destination = Path(
                    next(
                        s.split("=", 1)[1] for s in command if s.startswith("DESTDIR=")
                    )
                )
                (destination / "usr/bin").mkdir(parents=True)
                (destination / "usr/bin/postgres").write_bytes(b"server")
                self.assertTrue(
                    any(s.startswith("ZIC=") and "/native/" in s for s in command)
                )

        libc = Entry(stat.S_IFREG, b"musl")
        with patch.object(postgres.subprocess, "run", side_effect=build):
            result = postgres.build_postgres(src, compiler, "aarch64", libc, self.dir)
            count = len(calls)
            self.assertEqual(
                postgres.build_postgres(src, compiler, "aarch64", libc, self.dir),
                result,
            )
            self.assertEqual(len(calls), count)
        with tarfile.open(result) as artifact:
            self.assertEqual(artifact.extractfile("usr/bin/postgres").read(), b"server")
        with patch.object(
            postgres.subprocess,
            "run",
            side_effect=subprocess.CalledProcessError(1, "configure"),
        ):
            with self.assertRaisesRegex(RuntimeError, "guest PostgreSQL build failed"):
                postgres.build_postgres(src, compiler, "x86_64", libc, self.dir)

    def test_linux_and_macos_outputs_bind_the_selected_inputs(self):
        kernel_payload = bytearray(64)
        kernel_payload[56:60] = b"ARMd"
        import struct

        compressed = gzip.compress(bytes(kernel_payload))
        zboot = bytearray(28)
        zboot[4:8] = b"zimg"
        zboot[24:28] = b"gzip"
        struct.pack_into("<II", zboot, 8, len(zboot), len(compressed))
        for system in ("linux", "darwin"):
            with self.subTest(system=system), ExitStack() as mocks:
                output = self.dir / system
                output.mkdir()
                work = self.dir / (system + "-build")
                work.mkdir()
                supervisor = work / "init"
                supervisor.write_bytes(b"supervisor")
                inputs = {"base": {}, "boot": {}}
                pins = {
                    "architectures": {"arm64": inputs},
                    "postgres": {},
                    "compilers": {"aarch64-macos": {}},
                }
                fixture = self.dir / "pins.json"
                fixture.write_text(json.dumps(pins))
                mocks.enter_context(patch.object(assemble, "LOCK", fixture))
                mocks.enter_context(
                    patch.object(assemble.platform, "machine", return_value="arm64")
                )
                mocks.enter_context(
                    patch.object(assemble.platform, "system", return_value="Darwin")
                )
                mocks.enter_context(
                    patch.object(assemble, "acquire", return_value=fixture)
                )
                mocks.enter_context(
                    patch.object(assemble, "build_postgres", return_value=fixture)
                )
                mocks.enter_context(patch.object(assemble, "add_postgres"))
                mocks.enter_context(
                    patch.object(
                        assemble,
                        "base_userland",
                        return_value={
                            "lib/ld-musl-aarch64.so.1": Entry(stat.S_IFREG, b"musl")
                        },
                    )
                )

                def command(argv, **kwargs):
                    if argv[0] == "/usr/bin/bsdtar":
                        kwargs["stdout"].write(
                            zboot + compressed
                            if argv[-1] == "boot/vmlinuz-virt"
                            else b"modules"
                        )
                    if argv[0] == "go" and argv[1] == "run":
                        (Path(argv[-1]) / "boot.ko").write_bytes(b"driver")
                    if argv[0] == "xcrun":
                        Path(argv[argv.index("-o") + 1]).write_bytes(b"signed helper")
                    return SimpleNamespace(stderr="CDHash=" + "a" * 40)

                mocks.enter_context(
                    patch.object(assemble.subprocess, "run", side_effect=command)
                )
                assemble.assemble(
                    SimpleNamespace(
                        system=system,
                        arch="arm64",
                        guest_binary=supervisor,
                        release=False,
                        cache=work,
                    ),
                    work,
                    output,
                )
                manifest = json.loads((output / "manifest.json").read_text())
                self.assertEqual(
                    manifest["initramfs_sha256"], assemble.sha(output / "initramfs.gz")
                )
                if system == "darwin":
                    self.assertEqual((output / "kernel").read_bytes(), kernel_payload)
                    self.assertEqual(
                        manifest["kernel_sha256"], assemble.sha(output / "kernel")
                    )
                    self.assertEqual(
                        (output / "pw-service-vm.cdhash").read_text().strip(), "a" * 40
                    )
                else:
                    self.assertEqual(
                        (output / "rootfs/init").read_bytes(), b"supervisor"
                    )
                    self.assertTrue((output / "rootfs/work").is_dir())


if __name__ == "__main__":
    unittest.main()

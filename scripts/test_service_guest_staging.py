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
from unittest.mock import patch

with patch.object(
    sys, "path", [str(Path(__file__).parent / "service-guest"), *sys.path]
):
    import assemble
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
        self.dir = Path(temp.name)

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

    def test_package_verification_uses_pinned_image_measurements(self):
        source = self.dir / "guest"
        source.write_bytes(b"first-party supervisor")
        tree = {"init": Entry(stat.S_IFREG | 0o555, source.read_bytes())}
        pack(tree, self.dir / "initramfs.gz")
        manifest = {
            "schema": 1,
            "architecture": "amd64",
            "initramfs_sha256": assemble.sha(self.dir / "initramfs.gz"),
        }
        (self.dir / "manifest.json").write_text(json.dumps(manifest))
        self.assertEqual(len(manifest["initramfs_sha256"]), 64)


if __name__ == "__main__":
    unittest.main()

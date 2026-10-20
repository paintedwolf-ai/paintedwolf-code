"""Final-package comparison never treats a version or an unrelated manifest as proof."""
from dataclasses import replace
import hashlib
import io
import os
from pathlib import Path
import stat
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release.installed_cases.package_payload import (
    BoundedReader, Entry, MetadataMismatch, cpio_entries, inspect, name, tar_entries, verify,
)


def cpio_member(path, data=b"", mode=stat.S_IFREG | 0o644, inode=1, links=1, crc=False):
    raw = path.encode() + b"\0"
    fields = [inode, mode, 0, 0, links, 0, len(data), 0, 0, 0, 0, len(raw), sum(data) if crc else 0]
    header = (b"070702" if crc else b"070701") + "".join(f"{field:08x}" for field in fields).encode()
    return header + raw + bytes(-(len(header) + len(raw)) % 4) + data + bytes(-len(data) % 4)


def cpio_archive(*entries):
    return io.BytesIO(b"".join(entries) + cpio_member("TRAILER!!!"))


class InstalledPayloadTests(unittest.TestCase):
    def test_tar_records_files_modes_and_links_without_extracting_paths(self):
        raw = io.BytesIO()
        with tarfile.open(fileobj=raw, mode="w") as archive:
            member = tarfile.TarInfo("./opt/painted-wolf-code/bin/pw")
            member.size, member.mode = 4, 0o755
            archive.addfile(member, io.BytesIO(b"ELF!"))
            member = tarfile.TarInfo("usr/bin/pw")
            member.type, member.mode, member.linkname = tarfile.SYMTYPE, 0o777, "/opt/painted-wolf-code/bin/pw"
            archive.addfile(member)
        raw.seek(0)
        entries = tar_entries(BoundedReader(raw))
        self.assertEqual(entries["opt/painted-wolf-code/bin/pw"].sha256, hashlib.sha256(b"ELF!").hexdigest())
        self.assertEqual(entries["usr/bin/pw"].target, "/opt/painted-wolf-code/bin/pw")
        for path in ["../escape", "/absolute", "a/../b", "a//b", "a/./b"]:
            with self.subTest(path=path), self.assertRaises(ValueError):
                name(path)

    def test_tar_duplicate_special_file_and_oversized_metadata_refuse(self):
        for duplicate in (True, False):
            raw = io.BytesIO()
            with tarfile.open(fileobj=raw, mode="w") as archive:
                member = tarfile.TarInfo("same")
                if not duplicate:
                    member.type = tarfile.FIFOTYPE
                archive.addfile(member)
                if duplicate:
                    archive.addfile(member)
            raw.seek(0)
            with self.assertRaises(ValueError):
                tar_entries(BoundedReader(raw))
        with self.assertRaises(ValueError):
            BoundedReader(io.BytesIO()).read(2 << 20)

    def test_cpio_supports_data_last_hardlinks_and_validated_crc(self):
        entries = cpio_entries(BoundedReader(cpio_archive(
            cpio_member("./first", inode=17, links=2),
            cpio_member("second", b"payload", inode=17, links=2, crc=True),
            cpio_member("link", b"second", mode=stat.S_IFLNK | 0o777),
        )))
        self.assertEqual(entries["first"].kind, "hardlink")
        self.assertEqual(entries["first"].target, "second")
        self.assertEqual(entries["second"].sha256, hashlib.sha256(b"payload").hexdigest())
        self.assertEqual(entries["link"].target, "second")
        bad = bytearray(cpio_member("data", b"payload", crc=True))
        bad[-1] = 1  # Alter the payload itself; this entry has one byte of padding.
        bad[-2] ^= 1
        with self.assertRaisesRegex(ValueError, "checksum"):
            cpio_entries(BoundedReader(cpio_archive(bytes(bad))))

    def test_cpio_rejects_truncated_malformed_privileged_or_inconsistent_payloads(self):
        for raw in [b"short", cpio_member("../escape"),
                    cpio_member("suid", mode=stat.S_IFREG | 0o4755),
                    cpio_member("device", mode=stat.S_IFCHR | 0o644),
                    cpio_member("data")[:6] + b"+0000001" + cpio_member("data")[14:],
                    cpio_member("first", b"one", inode=10, links=2) + cpio_member("second", b"two", inode=10, links=2)]:
            with self.subTest(raw=raw[:16]), self.assertRaises(ValueError):
                cpio_entries(BoundedReader(io.BytesIO(raw + cpio_member("TRAILER!!!"))))

    def test_selected_package_hash_is_checked_before_any_decoder_executes(self):
        with tempfile.TemporaryDirectory() as directory:
            package = Path(directory) / "candidate.deb"
            package.write_bytes(b"unrelated package")
            with patch("scripts.linux_release.installed_cases.package_payload.subprocess.Popen") as launch:
                with self.assertRaisesRegex(ValueError, "selected artifact"):
                    inspect(package, "deb", "0" * 64)
                launch.assert_not_called()

    def test_installed_bytes_mode_and_symbolic_or_hard_links_must_match(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            file = root / "program"
            file.write_bytes(b"original")
            file.chmod(0o755)
            (root / "symbolic").symlink_to("program")
            os.link(file, root / "hard")
            uid, gid = file.stat().st_uid, file.stat().st_gid
            entry = Entry("program", 0o755, uid, gid, "file", 8, hashlib.sha256(b"original").hexdigest())
            entries = {"program": entry,
                       "symbolic": Entry("symbolic", 0o777, uid, gid, "symlink", target="program"),
                       "hard": replace(entry, name="hard", kind="hardlink", target="program")}
            self.assertEqual(verify(entries, root)["files_and_links"], 3)
            file.write_bytes(b"modified")
            with self.assertRaisesRegex(ValueError, "bytes"):
                verify(entries, root)
            file.write_bytes(b"original")
            file.chmod(0o644)
            with self.assertRaisesRegex(ValueError, "mode"):
                verify(entries, root)
            file.chmod(0o755)
            (root / "symbolic").unlink()
            (root / "symbolic").symlink_to("other")
            with self.assertRaisesRegex(ValueError, "symbolic link"):
                verify(entries, root)

    def test_metadata_refusal_identifies_only_the_selected_payload_path_and_numeric_facts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / "program"
            path.write_bytes(b"private fixture contents")
            path.chmod(0o644)
            actual = path.stat()
            expected = Entry("program", 0o755, actual.st_uid, actual.st_gid, "file")
            with self.assertRaises(MetadataMismatch) as error:
                verify({"program": expected}, root)
            self.assertEqual(error.exception.facts, {"path": "program", "kind": "file",
                "expected": {"mode": 0o755, "uid": actual.st_uid, "gid": actual.st_gid},
                "observed": {"mode": 0o644, "uid": actual.st_uid, "gid": actual.st_gid}})
            self.assertNotIn("private", repr(error.exception.facts))

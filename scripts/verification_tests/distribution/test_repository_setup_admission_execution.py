"""Repository setup archive admission rejects executable and unowned surfaces."""

import io
import hashlib
from pathlib import Path
import stat
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release.repository_setup_admission import snapshot
from scripts.linux_release.repository_setup_identity import package_identity
from scripts.linux_release.repository_setup_archives import cpio, payload, tar_members, rpm_files


def archive(name="etc/config", mode=0o644, kind=tarfile.REGTYPE, uid=0, duplicate=False):
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w") as stream:
        for _ in range(2 if duplicate else 1):
            member = tarfile.TarInfo(name)
            member.mode, member.uid, member.type = mode, uid, kind
            member.linkname = "target"
            member.size = 1 if kind == tarfile.REGTYPE else 0
            stream.addfile(member, io.BytesIO(b"x") if member.size else None)
    return output.getvalue()


def newc(name, body=b"x", mode=stat.S_IFREG | 0o644, uid=0, links=1):
    path = name.encode() + b"\0"
    fields = [1, mode, uid, 0, links, 0, len(body), 0, 0, 0, 0, len(path), 0]
    data = b"070701" + b"".join(f"{field:08x}".encode() for field in fields) + path
    data += b"\0" * (-len(data) % 4)
    return data + body + b"\0" * (-len(body) % 4)


class SetupArchiveAdmission(unittest.TestCase):
    def test_tar_exact_data_and_unsafe_members(self):
        self.assertEqual(tar_members(archive()), {"etc/config": b"x"})
        for changed in [{"name": "../escape"}, {"name": "/etc/config"}, {"uid": 1},
                        {"mode": 0o755}, {"kind": tarfile.SYMTYPE}, {"kind": tarfile.LNKTYPE},
                        {"kind": tarfile.FIFOTYPE}, {"duplicate": True}]:
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                tar_members(archive(**changed))

    def test_exact_payload_refuses_extra_files_and_unrelated_directories(self):
        payload({"etc": None, "etc/config": b"x"}, {"etc/config": b"x"})
        for records in [{"etc/config": b"changed"}, {"etc/config": b"x", "extra": b"x"},
                        {"etc/config": b"x", "unowned": None}]:
            with self.subTest(records=records), self.assertRaises(ValueError):
                payload(records, {"etc/config": b"x"})

    def test_newc_exact_data_rejects_unsafe_headers_and_trailing_bytes(self):
        trailer = newc("TRAILER!!!", b"")
        self.assertEqual(cpio(newc("./etc/config") + trailer), {"etc/config": b"x"})
        for changed in [{"name": "../escape"}, {"name": "/etc/config"}, {"uid": 1},
                        {"mode": stat.S_IFREG | 0o755}, {"mode": stat.S_IFLNK | 0o644}, {"links": 2}]:
            args = {"name": "etc/config", **changed}
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                cpio(newc(**args) + trailer)
        for data in [newc("etc/config") * 2 + trailer, newc("etc/config"), trailer + b"unexpected"]:
            with self.assertRaises(ValueError):
                cpio(data)

    def test_snapshot_refuses_links_and_oversize(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source"
            source.write_bytes(b"x")
            snapshot(source, root / "retained", 1)
            self.assertEqual((root / "retained").read_bytes(), b"x")
            with self.assertRaises(ValueError):
                snapshot(source, root / "oversize", 0)
            (root / "link").symlink_to(source)
            with self.assertRaises(OSError):
                snapshot(root / "link", root / "linked", 1)

    def test_setup_identity_binds_channel_and_generation_without_changing_app_version(self):
        deb = package_identity("1.2.3", "deb", "preview", 2)
        rpm = package_identity("1.2.3", "rpm", "preview", 2)
        self.assertEqual(deb["version"], "1.2.3+repo.2.preview")
        self.assertEqual(deb["file"], "painted-wolf-code-repo_1.2.3+repo.2.preview_all.deb")
        self.assertIsNone(deb["release"])
        self.assertEqual((rpm["version"], rpm["release"], rpm["architecture"]), ("1.2.3", "2.preview", "noarch"))
        self.assertEqual(rpm["file"], "painted-wolf-code-repo-1.2.3-2.preview.noarch.rpm")
        for family in ["deb", "rpm"]:
            first = package_identity("1.2.3", family, "preview", 1)
            self.assertNotEqual(first["file"], package_identity("1.2.3", family, "preview", 2)["file"])
            self.assertNotEqual(first["file"], package_identity("1.2.3", family, "stable", 1)["file"])
        for generation in [True, 0, -1, 1.0, "1"]:
            with self.subTest(generation=generation), self.assertRaises(ValueError):
                package_identity("1.2.3", "deb", "preview", generation)
        with self.assertRaises(ValueError):
            package_identity("1.2.3", "pacman", "preview", 1)
        with self.assertRaises(ValueError):
            package_identity("1.2.3", "deb", "arbitrary", 1)

    def test_rpm_header_file_digests_and_sizes_must_match_actual_selected_payload(self):
        expected = {"etc/config": b"x", "etc/key": b"y"}
        owned = b"/etc/config\t33188\t17\troot\troot\n/etc/key\t33188\t0\troot\troot\n"
        metadata = "".join(f"/{name}\t{len(data)}\t{hashlib.sha256(data).hexdigest()}\n"
                           for name, data in expected.items()).encode()
        responses = [b"", b"", owned, b"8", metadata]
        with patch("scripts.linux_release.repository_setup_archives.command", side_effect=responses) as query:
            rpm_files(Path("setup.rpm"), expected, "etc/config")
            self.assertEqual(query.call_count, 5)
        for altered in [[b"unexpected capability", *responses[1:]],
                        [*responses[:3], b"1", metadata],
                        [*responses[:4], metadata.replace(b"\t1\t", b"\t2\t", 1)],
                        [*responses[:4], metadata.replace(hashlib.sha256(b"x").hexdigest().encode(), b"0" * 64)]]:
            with self.subTest(altered=altered), patch(
                    "scripts.linux_release.repository_setup_archives.command", side_effect=altered), self.assertRaises(ValueError):
                rpm_files(Path("setup.rpm"), expected, "etc/config")

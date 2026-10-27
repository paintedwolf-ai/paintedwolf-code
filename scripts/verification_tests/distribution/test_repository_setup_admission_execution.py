"""Setup packages carry only reviewed trust and configuration; admission rereads every native fact."""

import hashlib
import io
from pathlib import Path
import shutil
import stat
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release.native_repositories import assemble
from scripts.linux_release.openpgp import ephemeral
from scripts.linux_release.repository_setup import build
from scripts.linux_release.repository_setup_admission import admit_setup, snapshot
from scripts.linux_release.repository_setup_archives import command, cpio, payload, rpm_files, tar_members
from scripts.linux_release.repository_setup_identity import package_identity
from scripts.linux_release.signing import digest, sign
from scripts.verification_tests.distribution.native_repository_fixtures import (
    PACKAGE, TESTDATA, manifest, native_tools)

ORIGIN = "https://repository.example.invalid"
AUTHORITY = ["family", "source_commit", "version", "channel", "repository_generation", "inventory_sha256",
             "signing_generation", "key_fingerprint"]


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


class RepositorySetupTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        native_tools("dpkg-deb", "apt-ftparchive", "rpm", "rpmbuild", "rpmsign", "rpmkeys", "rpm2cpio",
                     "createrepo_c", "gpg", "gpgv")
        cls.temporary = tempfile.TemporaryDirectory()
        root = Path(cls.temporary.name)
        cls.catalog = ephemeral(root / "trust")
        cls.private = cls.catalog.parent / "rehearsal-secret.asc"
        cls.generations = {}
        for family in ["deb", "rpm"]:
            directory = root / family
            receipt = assemble(family, manifest(family, TESTDATA / "inputs"), TESTDATA / "inputs", directory,
                               "preview", cls.catalog, cls.private, True, PACKAGE)
            cls.generations[family] = receipt, directory

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)

    def build(self, family, destination=None, url=ORIGIN, rehearsal=True):
        receipt, directory = self.generations[family]
        destination = destination or self.work / (family + "-setup")
        return build(receipt, directory, self.catalog, destination, url, self.private, rehearsal), destination

    def admit(self, receipt, output, package=None, **changes):
        authority = {name: receipt[name] for name in AUTHORITY}
        package = package or output / receipt["package"]["file"]
        return admit_setup(package, output / "SHA256SUMS", output / "SHA256SUMS.asc", self.catalog,
                           **{**authority, "base_url": ORIGIN, **changes}, rehearsal=True)

    def test_built_setup_packages_admit_with_exact_installed_files(self):
        for family in ["deb", "rpm"]:
            with self.subTest(family=family):
                receipt, output = self.build(family)
                verified = self.admit(receipt, output)
                self.assertEqual(verified["package"], receipt["package"])
                self.assertEqual(verified["installed_files"], receipt["installed_files"])
                self.assertEqual(sorted(path.name for path in output.iterdir()),
                                 sorted([receipt["package"]["file"], "SHA256SUMS", "SHA256SUMS.asc",
                                         "package-key.asc", "setup.json"]))

    def test_setup_build_refuses_unsafe_origins_mutable_destinations_and_production_trust(self):
        receipt, directory = self.generations["deb"]
        for url, destination, reason in [("https://example.invalid/\nenabled=0", None, "explicit HTTPS origin"),
                                         ("http://example.invalid", None, "explicit HTTPS origin"),
                                         ("https://user@example.invalid", None, "explicit HTTPS origin"),
                                         (ORIGIN, directory / "setup", "immutable repository generation")]:
            with self.subTest(url=url, reason=reason), self.assertRaisesRegex(ValueError, reason):
                self.build("deb", destination, url)
            self.assertFalse((destination or self.work / "deb-setup").exists())
        with self.assertRaisesRegex(ValueError, "cross-trust"):
            self.build("deb", rehearsal=False)
        existing = self.work / "existing"
        existing.mkdir()
        with self.assertRaisesRegex(ValueError, "new output directory"):
            self.build("deb", existing)

    def test_admission_refuses_mismatched_independent_authority(self):
        receipt, output = self.build("rpm")
        for changes, reason in [({"base_url": "https://wrong-origin.invalid"}, "payload sizes or digests"),
                                ({"channel": "stable"}, "filename differs"),
                                ({"signing_generation": receipt["signing_generation"] + 1}, "independently selected trust"),
                                ({"key_fingerprint": "0" * 40}, "independently selected trust"),
                                ({"source_commit": "arbitrary"}, "explicit source"),
                                ({"repository_generation": "arbitrary"}, "explicit source"),
                                ({"base_url": "http://repository.example.invalid"}, "explicit HTTPS origin"),
                                ({"family": "pacman"}, "explicit source")]:
            with self.subTest(changes=changes), self.assertRaisesRegex(ValueError, reason):
                self.admit(receipt, output, **changes)

    def test_admission_refuses_links_changed_checksums_and_foreign_signatures(self):
        receipt, output = self.build("deb")
        package = output / receipt["package"]["file"]
        linked = self.work / package.name
        linked.symlink_to(package)
        with self.assertRaises(OSError):
            self.admit(receipt, output, linked)
        checksums = output / "SHA256SUMS"
        original = checksums.read_bytes()
        checksums.write_text("0" * 64 + "  " + package.name + "\n")
        with self.assertRaisesRegex(ValueError, "checksums differ"):
            self.admit(receipt, output)
        checksums.write_bytes(original)
        other = ephemeral(self.work / "other")
        sign("detached", checksums, output / "SHA256SUMS.asc", other, other.parent / "rehearsal-secret.asc", True)
        with self.assertRaisesRegex(ValueError, "gpgv failed"):
            self.admit(receipt, output)

    def test_correctly_signed_hostile_deb_contents_are_refused(self):
        receipt, output = self.build("deb")
        artifact = output / receipt["package"]["file"]
        config = "etc/apt/sources.list.d/painted-wolf-code.sources"
        for kind, reason in [("script", "unsafe mode"), ("payload", "payload differs"), ("mode", "unsafe mode"),
                             ("conffile", "conffiles"), ("relationship", "relationships")]:
            with self.subTest(kind=kind):
                tree = self.work / ("hostile-" + kind)
                subprocess.run(["dpkg-deb", "--raw-extract", str(artifact), str(tree)], check=True)
                if kind == "script":
                    (tree / "DEBIAN/postinst").write_text("#!/bin/sh\nexit 0\n")
                    (tree / "DEBIAN/postinst").chmod(0o755)
                elif kind == "payload":
                    (tree / "etc/unowned").write_text("extra\n")
                    (tree / "etc/unowned").chmod(0o644)
                elif kind == "mode":
                    (tree / config).chmod(0o666)
                elif kind == "conffile":
                    (tree / "DEBIAN/conffiles").write_text("/etc/apt/keyrings/painted-wolf-code.asc\n")
                else:
                    control = tree / "DEBIAN/control"
                    control.write_text(control.read_text() + "Conflicts: unrelated-application\n")
                hostile = self.signed_stage(kind)
                subprocess.run(["dpkg-deb", "--build", "--root-owner-group", str(tree), str(hostile / artifact.name)],
                               check=True, stdout=subprocess.DEVNULL)
                self.refuse_hostile(receipt, hostile, artifact.name, reason)

    def test_correctly_signed_hostile_rpm_contents_are_refused(self):
        receipt, output = self.build("rpm")
        artifact = output / receipt["package"]["file"]
        payload = cpio(command(["rpm2cpio", str(artifact)]))
        native = package_identity(receipt["version"], "rpm", receipt["channel"], receipt["signing_generation"])
        for kind, reason in [("script", "scripts"), ("flags", "configuration flags"),
                             ("relationship", "scripts or triggers")]:
            with self.subTest(kind=kind):
                top = self.work / ("hostile-rpm-" + kind)
                tree = top / "payload"
                for path, data in payload.items():
                    (tree / path).parent.mkdir(parents=True, exist_ok=True)
                    (tree / path).write_bytes(data)
                    (tree / path).chmod(0o644)
                for directory in [tree, *tree.rglob("*")]:
                    if directory.is_dir():
                        directory.chmod(0o755)
                for name in ["BUILD", "RPMS", "SOURCES", "SPECS", "SRPMS", "BUILDROOT"]:
                    (top / name).mkdir(parents=True, exist_ok=True)
                relation = "Obsoletes: unrelated-application\n" if kind == "relationship" else ""
                script = "\n%post\nexit 0\n" if kind == "script" else ""
                config = "" if kind == "flags" else "%config(noreplace) "
                (top / "SPECS/setup.spec").write_text(
                    f"Name: painted-wolf-code-repo\nVersion: {native['version']}\nRelease: {native['release']}\n"
                    "Summary: Isolated hostile setup fixture\nLicense: Apache-2.0\nBuildArch: noarch\nAutoReqProv: no\n"
                    f"{relation}%description\nIsolated hostile setup fixture, never installed.\n%install\n"
                    f"mkdir -p %{{buildroot}}\ncp -a {tree}/. %{{buildroot}}/\n%files\n"
                    f"{config}/etc/yum.repos.d/painted-wolf-code.repo\n/etc/pki/rpm-gpg/painted-wolf-code.asc\n{script}")
                subprocess.run(["rpmbuild", "--define", "_topdir " + str(top), "--define",
                                "_binary_filedigest_algorithm 8", "-bb", str(top / "SPECS/setup.spec")],
                               check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                hostile = self.signed_stage(kind)
                package = hostile / artifact.name
                shutil.copyfile(next((top / "RPMS").rglob("*.rpm")), package)
                sign("rpm", package, package, self.catalog, self.private, True)
                self.refuse_hostile(receipt, hostile, artifact.name, reason)

    def signed_stage(self, kind):
        stage = self.work / ("signed-" + kind)
        stage.mkdir(exist_ok=True)
        return stage

    def refuse_hostile(self, receipt, stage, name, reason):
        package = stage / name
        (stage / "SHA256SUMS").write_text(digest(package) + "  " + name + "\n")
        sign("detached", stage / "SHA256SUMS", stage / "SHA256SUMS.asc", self.catalog, self.private, True)
        with self.assertRaisesRegex(ValueError, reason):
            self.admit(receipt, stage, package)


if __name__ == "__main__":
    unittest.main()

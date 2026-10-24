"""Selected campaign ZIPs cannot replace files or escape their retained root."""

from contextlib import nullcontext
import hashlib
from pathlib import Path
import stat
import tempfile
import unittest
from unittest.mock import patch
import warnings
import zipfile

from linux_release.installed_artifact_bundle import Limits, extract


class PublicationBundleTests(unittest.TestCase):
    def archive(self, root, entries):
        archive = root / "campaign.zip"
        with warnings.catch_warnings(), zipfile.ZipFile(archive, "w") as output:
            warnings.simplefilter("ignore", UserWarning)
            for name, value in entries:
                output.writestr(name, value)
        return archive, hashlib.sha256(archive.read_bytes()).hexdigest()

    def test_exact_archive_retains_package_and_evidence_bytes_without_executable_modes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive, digest = self.archive(root, [("evidence/", b""), ("evidence/probe", b"observed"),
                                                 ("packages/app.rpm", b"signed bytes")])
            destination = root / "retained"
            extract(archive, destination, digest)
            self.assertEqual((destination / "packages/app.rpm").read_bytes(), b"signed bytes")
            self.assertEqual((destination / "evidence/probe").read_bytes(), b"observed")
            self.assertEqual(stat.S_IMODE((destination / "packages/app.rpm").stat().st_mode), 0o600)
            with self.assertRaisesRegex(ValueError, "fresh"):
                extract(archive, destination, digest)

    def test_digest_mismatch_allocates_no_destination(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive, _ = self.archive(root, [("file", b"original")])
            with self.assertRaisesRegex(ValueError, "server artifact digest"):
                extract(archive, root / "out", "0" * 64)
            self.assertFalse((root / "out").exists())

    def test_unsafe_duplicate_and_conflicting_entries_refuse_before_extraction(self):
        link = zipfile.ZipInfo("link")
        link.create_system = 3
        link.external_attr = (stat.S_IFLNK | 0o777) << 16
        for entries in [[("../escape", b"bad")], [("/escape", b"bad")], [("a\\escape", b"bad")],
                        [("a//b", b"bad")], [(link, b"outside")],
                        [("same", b"a"), ("same", b"b")], [("a", b"file"), ("a/child", b"bad")]]:
            with self.subTest(entries=entries), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                archive, digest = self.archive(root, entries)
                with self.assertRaises(ValueError):
                    extract(archive, root / "out", digest)
                self.assertFalse((root / "out").exists())

    def test_declared_bounds_and_disk_reserve_refuse_without_partial_destination(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive, digest = self.archive(root, [("a", b"content")])
            with self.assertRaisesRegex(ValueError, "size bound"):
                extract(archive, root / "out", digest,limits=Limits(max_bytes=1))
            with patch("linux_release.installed_artifact_bundle.shutil.disk_usage") as usage:
                usage.return_value.free = 1
                with self.assertRaisesRegex(ValueError, "free disk"):
                    extract(archive, root / "out", digest)
            self.assertFalse((root / "out").exists())


class ControllerInputRetentionTests(unittest.TestCase):
    def test_retention_copies_exact_declared_roles_with_private_modes(self):
        from linux_release.installed_artifact_bundle import retain_tree
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root/"source"
            (source/"packages").mkdir(parents=True)
            (source/"manifest.json").write_bytes(b"manifest")
            (source/"packages/app").write_bytes(b"package")
            retain_tree(source, root/"retained", root_files={"manifest.json"}, root_dirs={"packages"})
            (source/"packages/app").write_bytes(b"changed")
            self.assertEqual((root/"retained/packages/app").read_bytes(),b"package")
            self.assertEqual((root/"retained/packages/app").stat().st_mode & 0o777,0o600)

    def test_unexpected_roles_links_and_resource_bounds_remove_owned_copy(self):
        from linux_release.installed_artifact_bundle import retain_tree
        for mutation in ("unexpected","link","count","disk"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                source = root/"source"
                (source/"packages").mkdir(parents=True)
                (source/"manifest.json").write_bytes(b"manifest")
                (source/"packages/app").write_bytes(b"package")
                if mutation=="unexpected":
                    (source/"receipt.json").write_bytes(b"receipt")
                if mutation=="link":
                    (source/"packages/link").symlink_to("app")
                target="linux_release.installed_artifact_bundle."
                manager=nullcontext() if mutation=="count" else patch(target+"shutil.disk_usage",return_value=type("Disk",(),{"free":0 if mutation=="disk" else 1<<40})())
                with manager,self.assertRaises((ValueError,OSError)):
                    retain_tree(source,root/"retained",root_files={"manifest.json"},root_dirs={"packages"},
                                limits=Limits(max_files=1) if mutation=="count" else Limits())
                self.assertFalse((root/"retained").exists())


class ImmutableCampaignLimitsTests(unittest.TestCase):
    def test_limits_are_explicit_immutable_and_cannot_raise_the_aggregate_byte_cap(self):
        from dataclasses import FrozenInstanceError
        selected=Limits(max_files=250000)
        with self.assertRaises(FrozenInstanceError):
            selected.max_files=1
        for arguments in ({"max_files":True},{"max_files":0},{"max_bytes":65<<30},{"max_bytes":0}):
            with self.subTest(arguments=arguments),self.assertRaises(ValueError):
                Limits(**arguments)

    def test_selected_entry_capacity_applies_before_any_extraction(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            archive=root/"input.zip"
            with zipfile.ZipFile(archive,"w") as output:
                output.writestr("first",b"one")
                output.writestr("second",b"two")
            digest=hashlib.sha256(archive.read_bytes()).hexdigest()
            with self.assertRaisesRegex(ValueError,"entry count"):
                extract(archive,root/"refused",digest,limits=Limits(max_files=1))
            extract(archive,root/"retained",digest,limits=Limits(max_files=2))
            self.assertEqual((root/"retained/second").read_bytes(),b"two")

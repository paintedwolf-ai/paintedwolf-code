"""Pinned search references share verified inputs across local and hosted checks."""

import hashlib
import io
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest import mock

from ci_policy import search_reference as reference
from ci_policy import runner_tests


def release_fixture(kind=tarfile.REGTYPE, extra=False):
    data = io.BytesIO()
    target = "fixture"
    with tarfile.open(fileobj=data, mode="w:gz") as bundle:
        member = tarfile.TarInfo(f"ripgrep-{reference.VERSION}-{target}/rg")
        member.type = kind
        member.linkname = "../outside"
        content = b"verified executable"
        member.size = len(content) if kind == tarfile.REGTYPE else 0
        bundle.addfile(member, io.BytesIO(content) if member.size else None)
        if extra:
            member = tarfile.TarInfo("../outside")
            member.size = 6
            bundle.addfile(member, io.BytesIO(b"unused"))
    archive = data.getvalue()
    return archive, reference.Release(target, hashlib.sha256(archive).hexdigest())


class SearchReferenceInputsTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.destination = self.root / "reference with spaces"

    def install(self, archive, release):
        fetch = mock.Mock(side_effect=lambda url, path: path.write_bytes(archive))
        binary = reference.ensure(self.destination, release, fetch)
        return binary, fetch

    def test_installs_exact_member_and_reuses_verified_archive(self):
        archive, release = release_fixture(extra=True)
        binary, fetch = self.install(archive, release)
        self.assertEqual(binary.read_bytes(), b"verified executable")
        self.assertEqual(binary.stat().st_mode & 0o777, 0o755)
        self.assertFalse((self.root / "outside").exists())
        self.assertEqual((self.destination / "release.tar.gz").read_bytes(), archive)
        fetch.assert_called_once()
        forbidden_fetch = mock.Mock(side_effect=AssertionError("cache refetched"))
        self.assertEqual(reference.ensure(self.destination, release, forbidden_fetch), binary)
        forbidden_fetch.assert_not_called()

    def test_bad_digest_is_refused_before_archive_parsing(self):
        with mock.patch.object(reference.tarfile, "open") as unpack:
            with self.assertRaises(ValueError):
                self.install(b"untrusted bytes", reference.Release("fixture", "0" * 64))
        unpack.assert_not_called()
        self.assertEqual(list(self.root.iterdir()), [])

    def test_links_and_directories_never_supply_the_executable(self):
        for kind in (tarfile.SYMTYPE, tarfile.LNKTYPE, tarfile.DIRTYPE):
            with self.subTest(kind=kind):
                archive, release = release_fixture(kind)
                with self.assertRaises(ValueError):
                    self.install(archive, release)
                self.assertFalse(self.destination.exists())

    def test_cache_tampering_is_refused_without_replacing_files(self):
        archive, release = release_fixture()
        binary, _ = self.install(archive, release)
        binary.write_bytes(b"changed")
        with self.assertRaises(ValueError):
            reference.ensure(self.destination, release)
        self.assertEqual(binary.read_bytes(), b"changed")
        self.assertEqual((self.destination / "release.tar.gz").read_bytes(), archive)

    def test_unknown_destination_and_symlinks_remain_untouched(self):
        archive, release = release_fixture()
        self.destination.mkdir()
        retained = self.destination / "keep"
        retained.write_bytes(b"owned elsewhere")
        with self.assertRaises(ValueError):
            self.install(archive, release)
        self.assertEqual(retained.read_bytes(), b"owned elsewhere")
        link = self.root / "link"
        link.symlink_to(self.destination, target_is_directory=True)
        with self.assertRaises(ValueError):
            reference.ensure(link, release)
        self.assertTrue(link.is_symlink())

    def test_archive_and_member_sizes_are_bounded(self):
        archive, release = release_fixture()
        with mock.patch.object(reference, "MAX_ARCHIVE_BYTES", len(archive) - 1):
            with self.assertRaises(ValueError):
                self.install(archive, release)
        with mock.patch.object(reference, "MAX_BINARY_BYTES", 1):
            with self.assertRaises(ValueError):
                self.install(archive, release)
        self.assertFalse(self.destination.exists())

    def test_corrupt_cache_archive_is_not_repaired_implicitly(self):
        archive, release = release_fixture()
        binary, _ = self.install(archive, release)
        (self.destination / "release.tar.gz").write_bytes(b"wrong archive")
        with self.assertRaises(ValueError):
            reference.ensure(self.destination, release)
        self.assertEqual(binary.read_bytes(), b"verified executable")

    def test_supported_platforms_resolve_declared_pins(self):
        for system, machine in reference.PINS:
            with self.subTest(system=system, machine=machine):
                with mock.patch.object(reference.platform, "system", return_value=system), mock.patch.object(reference.platform, "machine", return_value=machine):
                    self.assertEqual(reference.host_release(), reference.Release(*reference.PINS[system, machine]))
        with mock.patch.object(reference.platform, "system", return_value="unknown"):
            with self.assertRaises(ValueError):
                reference.host_release()

    def test_download_keeps_paths_literal(self):
        destination = self.root / "output; literal archive"
        with mock.patch.object(reference.subprocess, "run") as run:
            reference.download("https://example.test/fixed-release", destination)
        args, kwargs = run.call_args
        self.assertIn(str(destination), args[0])
        self.assertEqual(args[0][-1], "https://example.test/fixed-release")
        self.assertNotIn("shell", kwargs)
        self.assertTrue(kwargs["check"])

    def test_cli_requires_one_destination_and_uses_the_host_pin(self):
        with self.assertRaises(ValueError):
            reference.main([])
        with self.assertRaises(ValueError):
            reference.main(["one", "two"])
        release = reference.Release("fixture", "0" * 64)
        with mock.patch.object(reference, "host_release", return_value=release), mock.patch.object(reference, "ensure", return_value=self.destination / "rg") as ensure, mock.patch("builtins.print"):
            reference.main([str(self.destination)])
        ensure.assert_called_once_with(str(self.destination), release)


class VerificationPythonInputsTest(unittest.TestCase):
    def test_discovery_loads_pinned_yaml_and_preserves_selection(self):
        with mock.patch.object(runner_tests.python_tools, "prepared", return_value=Path("/fixture/packages")), mock.patch.object(runner_tests.unittest, "main") as run, mock.patch.object(runner_tests.sys, "path", []):
            runner_tests.main(["-k", "chosen case"])
            self.assertEqual(runner_tests.sys.path[:3], ["/fixture/packages", str(runner_tests.SCRIPTS.parent), str(runner_tests.SCRIPTS)])
        self.assertEqual(run.call_args.kwargs["argv"][-2:], ["-k", "chosen case"])
        self.assertEqual(run.call_args.kwargs["module"], None)

    def test_missing_preparation_fails_before_discovery(self):
        with mock.patch.object(runner_tests.python_tools, "prepared", side_effect=ValueError("not prepared")), mock.patch.object(runner_tests.unittest, "main") as run:
            with self.assertRaises(SystemExit):
                runner_tests.main([])
        run.assert_not_called()

    def test_unsupported_interpreter_fails_before_dependency_loading(self):
        with mock.patch.object(runner_tests.sys, "version_info", (3, 9)), mock.patch.object(runner_tests.python_tools, "prepared") as prepare:
            with self.assertRaises(SystemExit):
                runner_tests.main([])
        prepare.assert_not_called()


if __name__ == "__main__":
    unittest.main()

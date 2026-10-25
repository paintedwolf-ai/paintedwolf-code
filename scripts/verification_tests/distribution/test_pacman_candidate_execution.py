"""Candidate packages bind source/family bytes and refuse unsafe substrate or archive inputs."""

import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from linux_release.arch_substrate import build, rootfs
from linux_release.arch_stage import extract, package_filter
from linux_release.candidate_inventory import inventory

ROOT = Path(__file__).resolve().parents[3]


class PacmanCandidateTests(unittest.TestCase):
    def test_final_pacman_metadata_is_checked_after_safe_extraction(self):
        class Decoder:
            def __init__(self, payload):
                self.stdout = io.BytesIO(payload)

            def wait(self):
                return 0

            def poll(self):
                return 0

        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            root = work / "source"
            (root / "lycaon/internal/platformfloor").mkdir(parents=True)
            (root / "lycaon/internal/platformfloor/linux_floor.txt").write_text("GLIBC 2.35\n")
            (root / "packaging/linux").mkdir(parents=True)
            (root / "packaging/linux/dependencies.json").write_text(json.dumps({
                "pacman": {"depends": ["glibc>={GLIBC}"], "recommends": []}}))
            (root / "RELEASE_BUILD").write_text("2\n")
            package = work / "candidate.pkg.tar.zst"
            package.write_bytes(b"selected native package")

            def archive_bytes(metadata):
                result = io.BytesIO()
                with tarfile.open(fileobj=result, mode="w") as output:
                    for name, body in {".PKGINFO": metadata, ".BUILDINFO": "native build\n",
                                       ".MTREE": "native inventory\n", "usr/bin/pw": "sealed bytes"}.items():
                        encoded = body.encode()
                        member = tarfile.TarInfo(name)
                        member.size = len(encoded)
                        member.mode = 0o755 if name == "usr/bin/pw" else 0o644
                        output.addfile(member, io.BytesIO(encoded))
                return result.getvalue()

            expected = "pkgname = painted-wolf-code\npkgver = 1.2.3-2\narch = x86_64\ndepend = glibc>=2.35\n"
            with patch("linux_release.arch_stage.subprocess.Popen", return_value=Decoder(archive_bytes(expected))):
                fields = extract(package, work / "installed", root, "x86_64", "1.2.3")
            self.assertEqual(fields["depend"], ["glibc>=2.35"])
            self.assertEqual((work / "installed/usr/bin/pw").read_text(), "sealed bytes")
            with patch("linux_release.arch_stage.subprocess.Popen", return_value=Decoder(archive_bytes(
                    expected.replace("glibc>=2.35", "glibc>=2.34")))), self.assertRaisesRegex(ValueError, "dependencies"):
                extract(package, work / "wrong-dependency", root, "x86_64", "1.2.3")
            with patch("linux_release.arch_stage.subprocess.Popen", return_value=Decoder(archive_bytes(
                    expected.replace("pkgver = 1.2.3-2", "pkgver = 1.2.3-1")))), self.assertRaisesRegex(ValueError, "selected release"):
                extract(package, work / "wrong-version", root, "x86_64", "1.2.3")

    def test_every_native_family_binds_final_retained_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            for extension in ["deb", "rpm", "pkg.tar.zst"]:
                (directory / ("painted-wolf-code_v1.2.3_linux-aarch64." + extension)).write_bytes(extension.encode())
            result = inventory(directory, "linux-aarch64", "aarch64", "1.2.3", "a" * 40)
            self.assertEqual([row["family"] for row in result["packages"]], ["deb", "rpm", "pacman"])
            for row in result["packages"]:
                self.assertEqual(hashlib.sha256((directory / row["file"]).read_bytes()).hexdigest(), row["sha256"])
            self.assertFalse(result["published"])
            self.assertEqual(result["qualification_status"], "unverified")
            package = directory / result["packages"][-1]["file"]
            package.unlink()
            with self.assertRaisesRegex(ValueError, "every native"):
                inventory(directory, "linux-aarch64", "aarch64", "1.2.3", "a" * 40)
            package.symlink_to(directory / result["packages"][0]["file"])
            with self.assertRaises(ValueError):
                inventory(directory, "linux-aarch64", "aarch64", "1.2.3", "a" * 40)
            with self.assertRaises(ValueError):
                inventory(directory, "linux-x86_64", "aarch64", "1.2.3", "a" * 40)

    def test_substrate_refuses_cross_cpu_before_docker_or_download(self):
        with patch("linux_release.arch_substrate.platform.machine", return_value="x86_64"), patch(
                "linux_release.arch_substrate.command") as runner, self.assertRaisesRegex(ValueError, "matching native"):
            build(ROOT, Path("unused"), "aarch64", 123)
        runner.assert_not_called()

    def test_remote_daemon_cannot_route_a_native_request_through_another_cpu(self):
        with patch("linux_release.arch_substrate.platform.system", return_value="Linux"), patch(
                "linux_release.arch_substrate.platform.machine", return_value="x86_64"), patch(
                "linux_release.arch_substrate.command", return_value="linux/arm64") as runner:
            with self.assertRaisesRegex(ValueError, "daemon must use"):
                build(ROOT, Path("unused"), "x86_64", 123)
            self.assertEqual(runner.call_count, 1)
            self.assertEqual(runner.call_args.args[:2], ("docker", "version"))

    def test_preparation_preserves_pacman_sandbox_and_sets_landlock_prerequisite(self):
        calls=[]
        def command(*arguments):
            calls.append(arguments)
            if arguments[:2] == ("docker","version"):
                return "linux/amd64"
            if arguments[:3] == ("docker","image","inspect"):
                return "amd64"
            if arguments[:2] == ("docker","create"):
                return "owned-container"
            if arguments[:2] == ("docker","inspect"):
                return "0"
            return "fixture"
        with tempfile.TemporaryDirectory() as temporary, \
                patch("linux_release.arch_substrate.platform.system",return_value="Linux"), \
                patch("linux_release.arch_substrate.platform.machine",return_value="x86_64"), \
                patch("linux_release.arch_substrate.os.getuid",return_value=1000), \
                patch("linux_release.arch_substrate.os.getgid",return_value=1000), \
                patch("linux_release.arch_substrate.command",side_effect=command), \
                patch("linux_release.arch_substrate.subprocess.run"):
            result=build(ROOT,Path(temporary),"x86_64",123)
        preparation=next(call for call in calls if call[:2]==("docker","create"))
        self.assertIn("--security-opt=no-new-privileges",preparation)
        self.assertNotIn("--privileged",preparation)
        self.assertNotIn("--security-opt=seccomp=unconfined",preparation)
        self.assertNotIn("--disable-sandbox"," ".join(preparation))
        self.assertFalse(result["network_during_packaging"])

    def test_corrupt_cached_rootfs_never_reaches_signature_or_docker(self):
        with tempfile.TemporaryDirectory() as temporary:
            row = json.loads((ROOT / "packaging/linux/arch/substrates.json").read_text())["aarch64"]
            path = Path(temporary) / "paintedwolf/arch-substrates" / (row["rootfs_sha256"] + ".tar.gz")
            path.parent.mkdir(parents=True)
            path.write_bytes(b"changed archive")
            with patch.dict("os.environ", {"XDG_CACHE_HOME": temporary}), patch(
                    "linux_release.arch_substrate.command") as runner, self.assertRaisesRegex(ValueError, "reviewed pin"):
                rootfs(ROOT, row)
            runner.assert_not_called()

    def test_arm_rootfs_requires_the_reviewed_key_and_upstream_signature(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            source = work / "source/packaging/linux/arch"
            source.mkdir(parents=True)
            (source / "trusted.asc").write_text("public fixture key")
            (source / "rootfs.sig").write_text("retained fixture signature")
            archive_bytes = b"bounded fixture rootfs"
            fingerprint = "A" * 40
            row = {"rootfs_sha256": hashlib.sha256(archive_bytes).hexdigest(),
                   "rootfs_url": "https://example.invalid/rootfs.tar.gz", "key_file": "trusted.asc",
                   "signature_file": "rootfs.sig", "key_fingerprint": fingerprint}
            commands = []

            def command(*arguments):
                commands.append(arguments)
                if "--show-keys" in arguments:
                    return "fpr:::::::::" + fingerprint + ":\n"
                if arguments[0] == "gpgv":
                    return "[GNUPG:] VALIDSIG " + fingerprint
                return ""

            def download(_url, destination, _limit):
                destination.write_bytes(archive_bytes)

            with patch.dict("os.environ", {"XDG_CACHE_HOME": str(work / "cache")}), \
                    patch("linux_release.arch_substrate.download", side_effect=download) as fetch, \
                    patch("linux_release.arch_substrate.command", side_effect=command):
                selected = rootfs(work / "source", row)
                self.assertEqual(selected.read_bytes(), archive_bytes)
                self.assertEqual(rootfs(work / "source", row), selected)
                fetch.assert_called_once()
                self.assertTrue(any(call[0] == "gpgv" for call in commands))
                (source / "rootfs.sig").unlink()
                with self.assertRaisesRegex(ValueError, "retained upstream"):
                    rootfs(work / "source", row)

    def test_imported_arm_substrate_is_offline_for_packaging_and_cleans_up(self):
        calls = []

        def command(*arguments):
            calls.append(arguments)
            if arguments[:2] == ("docker", "version"):
                return "linux/arm64"
            if arguments[:3] == ("docker", "image", "inspect"):
                return "arm64"
            if arguments[:2] == ("docker", "create"):
                return "owned-arm-container"
            if arguments[:2] == ("docker", "inspect"):
                return "0"
            if arguments[-2:] == ("pacman", "-Q"):
                return "base-devel 1\nmakepkg 2"
            return "fixture"

        with tempfile.TemporaryDirectory() as temporary, \
                patch("linux_release.arch_substrate.platform.system", return_value="Linux"), \
                patch("linux_release.arch_substrate.platform.machine", return_value="aarch64"), \
                patch("linux_release.arch_substrate.os.getuid", return_value=1000), \
                patch("linux_release.arch_substrate.os.getgid", return_value=1000), \
                patch("linux_release.arch_substrate.rootfs", return_value=Path(temporary) / "verified-rootfs"), \
                patch("linux_release.arch_substrate.command", side_effect=command), \
                patch("linux_release.arch_substrate.subprocess.run") as runner:
            result = build(ROOT, Path(temporary), "aarch64", 123)
        self.assertEqual(result["platform"], "linux/arm64")
        self.assertFalse(result["network_during_packaging"])
        self.assertTrue(any(call[:2] == ("docker", "import") for call in calls))
        self.assertTrue(any(call.args[0][:3] == ["docker", "image", "rm"] for call in runner.call_args_list))
        self.assertTrue(any(call.args[0][:3] == ["docker", "rm", "--force"] for call in runner.call_args_list))

    def test_symbolic_links_are_relative_to_their_parent_and_cannot_escape(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            member = tarfile.TarInfo("usr/share/fixture/link")
            member.type = tarfile.SYMTYPE
            member.linkname = "../../../opt/painted-wolf-code/bin/pw"
            self.assertEqual(package_filter(member, root).linkname, member.linkname)
            for target in ["../../../../escape", "/opt/painted-wolf-code/bin/pw"]:
                member.linkname = target
                with self.subTest(target=target), self.assertRaisesRegex(ValueError, "escapes"):
                    package_filter(member, root)

    def test_archive_escape_and_duplicate_paths_never_escape_extraction(self):
        class Decoder:
            def __init__(self, payload):
                self.stdout = io.BytesIO(payload)
            def wait(self):
                return 0
            def poll(self):
                return 0

        for names in [["../escape"], ["same", "same"]]:
            with self.subTest(names=names), tempfile.TemporaryDirectory() as temporary:
                work = Path(temporary)
                archive = io.BytesIO()
                with tarfile.open(fileobj=archive, mode="w") as output:
                    for name in names:
                        member = tarfile.TarInfo(name)
                        member.size = 1
                        member.mode = 0o644
                        output.addfile(member, io.BytesIO(b"x"))
                package = work / "candidate.pkg.tar.zst"
                package.write_bytes(b"selected archive")
                with patch("linux_release.arch_stage.subprocess.Popen", return_value=Decoder(archive.getvalue())), self.assertRaisesRegex(ValueError, "unsafe or repeated"):
                    extract(package, work / "root", ROOT, "aarch64", "1.2.3")
                self.assertFalse((work / "escape").exists())

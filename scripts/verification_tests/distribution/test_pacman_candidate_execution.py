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
from linux_release.arch_stage import extract
from linux_release.candidate_inventory import inventory

ROOT = Path(__file__).resolve().parents[3]


class PacmanCandidateTests(unittest.TestCase):
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

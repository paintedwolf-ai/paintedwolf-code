"""Hosted Nix provisioning installs only the pinned toolchain and fetches locked inputs before offline lanes."""

import hashlib
import io
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release import nix_runner

HOSTED = {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted"}


class NixRunnerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.profile = self.root / "profile"
        patcher = patch.object(nix_runner, "PROFILE", self.profile)
        patcher.start()
        self.addCleanup(patcher.stop)
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:xz") as archive:
            script = b"#!/bin/sh\n"
            entry = tarfile.TarInfo(f"nix-{nix_runner.VERSION}-x86_64-linux/install")
            entry.size, entry.mode = len(script), 0o755
            archive.addfile(entry, io.BytesIO(script))
            # Store links in the real installer are absolute.
            link = tarfile.TarInfo(f"nix-{nix_runner.VERSION}-x86_64-linux/store/{'0' * 32}-lib/lib/libgcc_s.so")
            link.type, link.linkname = tarfile.SYMTYPE, f"/nix/store/{'0' * 32}-lib/lib/libgcc_s.so.1"
            archive.addfile(link)
        self.installer = buffer.getvalue()
        self.commands = []

    def fetch(self, url, destination):
        self.fetched = url
        Path(destination).write_bytes(self.installer)

    def run_installer(self, command, check):
        self.assertTrue(check)
        self.assertTrue(Path(command[0]).is_file())
        self.configuration = (Path(command[-1])).read_text()
        self.commands.append(command)
        return subprocess.CompletedProcess(command, 0)

    def install(self, environment, digest=None):
        digest = digest or hashlib.sha256(self.installer).hexdigest()
        with patch.dict(nix_runner.INSTALLERS, {"x86_64": digest}), \
                patch.object(nix_runner.platform, "system", return_value="Linux"), \
                patch.object(nix_runner.platform, "machine", return_value="x86_64"):
            return nix_runner.install(environment, fetch=self.fetch, run=self.run_installer)

    def test_hosted_runner_installs_the_verified_pinned_daemon_and_exports_its_profile(self):
        path = self.root / "github-path"
        self.assertEqual(self.install({**HOSTED, "GITHUB_PATH": str(path)}), self.profile)
        self.assertEqual(self.fetched, "https://releases.nixos.org/nix/nix-2.35.3/nix-2.35.3-x86_64-linux.tar.xz")
        self.assertEqual(self.commands[0][1:7], ["--daemon", "--yes", "--daemon-user-count", "4",
                                                 "--no-channel-add", "--no-modify-profile"])
        self.assertEqual(self.configuration, "sandbox = true\ntrusted-users = root\n")
        self.assertEqual(path.read_text(), f"{self.profile}\n")

    def test_a_changed_installer_or_an_unmanaged_host_never_installs(self):
        with self.assertRaisesRegex(ValueError, "digest"):
            self.install(HOSTED, digest="0" * 64)
        for environment in ({}, {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "self-hosted"}):
            with self.subTest(environment=environment), self.assertRaisesRegex(ValueError, "not installed"):
                self.install(environment)
        self.assertEqual(self.commands, [])

    def test_only_supported_linux_architectures_install(self):
        for system, machine in (("Darwin", "x86_64"), ("Linux", "riscv64")):
            with self.subTest(system=system, machine=machine), \
                    patch.object(nix_runner.platform, "system", return_value=system), \
                    patch.object(nix_runner.platform, "machine", return_value=machine), \
                    self.assertRaisesRegex(ValueError, "amd64 and arm64"):
                nix_runner.install(HOSTED, fetch=self.fetch, run=self.run_installer)
        self.assertEqual(self.commands, [])

    def test_an_existing_installation_is_used_without_downloading(self):
        self.profile.mkdir()
        (self.profile / "nix").write_text("")
        self.assertEqual(self.install({}), self.profile)
        self.assertFalse(hasattr(self, "fetched"))

    def test_prepare_fetches_flake_and_nonflake_inputs_with_the_installed_toolchain(self):
        seen = []

        def run(command, cwd, env, stdout):
            seen.append((command, env["PATH"].split(":")[0]))
            return subprocess.CompletedProcess(command, 0)

        self.assertEqual(nix_runner.prepare(["nix"], self.root, {"PATH": "/usr/bin"}, run=run,
                                            installer=lambda environment: self.profile), 0)
        self.assertEqual(seen[0][0], ["nix", "flake", "archive", "--no-write-lock-file", f"git+file://{self.root}"])
        self.assertIn(f"import {self.root}/packaging/nix/flake-compat.nix", seen[1][0][-1])
        self.assertTrue(all(first == str(self.profile) for _, first in seen))

    def test_prepare_stops_at_the_first_failed_fetch(self):
        seen = []

        def run(command, cwd, env, stdout):
            seen.append(command)
            return subprocess.CompletedProcess(command, 3)

        self.assertEqual(nix_runner.prepare(["nix"], self.root, {}, run=run, installer=lambda e: self.profile), 3)
        self.assertEqual(len(seen), 1)


if __name__ == "__main__":
    unittest.main()

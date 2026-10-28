"""A first Linux release withdraws to its signed empty generation through the real publishers."""

import json
from pathlib import Path
import shutil
import sys
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release import (native_repositories, pacman_r2, pacman_repository, release_repositories,
                                   repository_r2, repository_withdrawal)
from scripts.linux_release.openpgp import ephemeral
from scripts.linux_release.repository_objects import encoded
from scripts.linux_release.repository_operations import identity
from scripts.verification_tests.distribution.repository_fixture_support import native_tools

SOURCE = "a" * 40
BASE = "https://downloads.example.invalid"


class FirstReleaseWithdrawalTests(unittest.TestCase):
    """Generations are real signed assembly output; only the object store is in memory."""

    @classmethod
    def setUpClass(cls):
        if shutil.which("gpg") is None:
            if sys.platform == "linux":
                raise AssertionError("Linux distribution tests require gpg")
            raise unittest.SkipTest("signing requires gpg")
        cls.temporary = tempfile.TemporaryDirectory()
        work = Path(cls.temporary.name)
        cls.catalog = ephemeral(work / "key")
        cls.private = cls.catalog.parent / "rehearsal-secret.asc"
        # The empty generation is production trust: the release lane's key, not rehearsal trust.
        cls.catalog.write_text(json.dumps({**json.loads(cls.catalog.read_text()), "rehearsal": False}))

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.objects = {}
        for target in [release_repositories, repository_withdrawal]:
            patcher = patch.object(target, "CATALOG", self.catalog)
            patcher.start()
            self.addCleanup(patcher.stop)
        for module in [repository_r2, pacman_r2]:
            for name, replacement in [("current", self.current), ("put", self.put), ("public_bytes", self.public)]:
                patcher = patch.object(module, name, side_effect=replacement)
                patcher.start()
                self.addCleanup(patcher.stop)

    def current(self, key, directory):
        return json.loads(self.objects[key]) if key in self.objects else None

    def put(self, key, source, mutable=False):
        if not mutable and key in self.objects and self.objects[key] != source.read_bytes():
            raise ValueError("immutable collision")
        self.objects[key] = source.read_bytes()

    def public(self, url, source, mutable=False):
        if self.objects.get(url.removeprefix(BASE + "/")) != source.read_bytes():
            raise ValueError("public bytes differ")

    def fetch(self, base_url, row, directory):
        for item in row["receipt"]["objects"]:
            target = directory / item["path"]
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(self.objects[f"linux/generations/{row['replacement']}/{item['path']}"])
        (directory / "generation.json").write_bytes(encoded(row["receipt"]))

    def empty(self, family: str) -> tuple[dict, Path]:
        destination = self.work / (family + "-withdrawal")
        manifest = {"format_version": 1, "source_commit": SOURCE, "version": "1.2.3", "packages": []}
        if family == "pacman":
            receipt = pacman_repository.assemble({**manifest, "architecture": "x86_64"}, self.work, destination,
                                                 "stable", self.catalog, self.private)
        else:
            receipt = native_repositories.assemble(family, manifest, self.work, destination, "stable", self.catalog,
                                                   self.private, architectures=["amd64", "arm64"])
        return receipt, destination

    def withdraw_first_release(self, family: str):
        empty, directory = self.empty(family)
        self.assertEqual(empty["packages"], [])
        offer = {**empty, "packages": [{"file": "painted-wolf-code.pkg", "sha256": "0" * 64}],
                 "inventory_sha256": "0" * 64}
        qualified = []
        release_repositories.retain_withdrawal(empty, offer, directory, BASE, qualified.append,
                                               upload=self.put, verify=self.public)
        self.assertEqual(qualified, [SOURCE])
        # The halted first release is the active offer, with no earlier Linux generation.
        halted = {"deb": "c", "rpm": "d", "pacman": "e"}[family] * 64
        active = {"format_version": 1, "operation": "publish", "generation": halted, "previous": None,
                  **{key: offer[key] for key in ["family", "channel", "version", "source_commit", "inventory_sha256",
                                                 "packages", "signing_generation", "key_fingerprint", "rehearsal"]},
                  **({"architecture": "x86_64"} if family == "pacman" else {})}
        state = release_repositories.channel_key(offer)
        self.objects[state] = encoded(active)
        rows = repository_withdrawal.prepare("1.2.3", None, read=self.current)
        self.assertEqual([row["replacement"] for row in rows], [identity(empty)])
        for _ in range(2):  # an interrupted halt retries the same plan
            repository_withdrawal.apply(rows, BASE, fetch=self.fetch)
            record = json.loads(self.objects[state])
            self.assertEqual((record["operation"], record["generation"], record["previous"], record["packages"]),
                             ("withdraw", identity(empty), halted, []))
        self.assertIn("linux/withdrawn/" + halted + ".json", self.objects)
        marker = "linux/withdrawn-versions/" + family + "/stable/" + ("x86_64" if family == "pacman" else "all")
        self.assertIn(marker + "/1.2.3.json", self.objects)
        self.assertEqual(repository_withdrawal.prepare("1.2.3", None, read=self.current), [])

    def test_pacman_first_release_withdraws_to_signed_empty_databases(self):
        self.withdraw_first_release("pacman")
        self.assertEqual(json.loads(self.objects["linux/channels/pacman/stable/x86_64/current.json"])["version"], "1.2.3")

    def test_apt_and_dnf_first_release_withdraws_to_signed_empty_indexes(self):
        native_tools("apt-ftparchive", "createrepo_c", "gpg", "gpgv")
        for family in ["deb", "rpm"]:
            with self.subTest(family=family):
                self.withdraw_first_release(family)

    def test_withdrawal_generation_must_match_its_offer(self):
        empty, directory = self.empty("pacman")
        for change in [{"version": "1.2.4"}, {"architecture": "aarch64"}, {"key_fingerprint": "0" * 40}]:
            with self.subTest(change=change), self.assertRaisesRegex(ValueError, "differs from the offer"):
                release_repositories.retain_withdrawal(empty, {**empty, **change}, directory, BASE, lambda _s: None,
                                                       upload=self.put, verify=self.public)
        with self.assertRaisesRegex(ValueError, "differs from the offer"):
            release_repositories.retain_withdrawal({**empty, "packages": [{"file": "x"}]}, empty, directory, BASE,
                                                   lambda _s: None, upload=self.put, verify=self.public)
        self.assertEqual(self.objects, {})


if __name__ == "__main__":
    unittest.main()

"""First-release AUR availability is independent for stable and preview."""

import json
from pathlib import Path
import subprocess
import tempfile
import unittest

from scripts.linux_release.aur_delivery import FixturePublisher, RecipePublisher, plan
from scripts.linux_release.aur_withdrawal import PACKAGES, authority, withdraw


class FirstReleaseTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.origins = {}
        for channel, package in PACKAGES.items():
            directory = self.root / channel
            directory.mkdir()
            self.origins[package] = FixturePublisher(directory)

    def factory(self, root, package, _key, _hosts, authorize):
        def admit(selected, directory):
            FixturePublisher.fixture_authority(selected, directory)
            authorize(selected, directory)
        return RecipePublisher(root, self.origins[package].origin, admit)

    def halt(self, version, name):
        return withdraw(version, self.root / name, Path("key"), Path("hosts"), publisher_factory=self.factory)

    def publish(self, channel, version):
        package = PACKAGES[channel]
        directory = self.root / (channel + "-" + version)
        directory.mkdir()
        (directory / "PKGBUILD").write_text(f"pkgname={package}\npkgver={version}\npkgrel=1\n")
        (directory / ".SRCINFO").write_text(
            f"pkgbase = {package}\n\tpkgver = {version}\n\tpkgrel = 1\n\tarch = x86_64\n\npkgname = {package}\n")
        manifest = {"family": "pacman", "source_commit": "f" * 40, "version": version,
                    "channel": channel, "rehearsal": True, "architecture": "x86_64",
                    "inventory_sha256": "b" * 64, "signing_generation": 1, "key_fingerprint": "A" * 40}
        selected = plan({**manifest, "package": package}, [manifest], directory, "c" * 64)
        publisher = self.origins[package]
        before = publisher.head()
        result = publisher.publish(selected, directory, before)
        self.assertEqual(publisher.publish(selected, directory, before)["commit"], result["commit"])
        return result

    def count(self, channel):
        origin = self.origins[PACKAGES[channel]].origin
        return int(subprocess.check_output(["git", "--git-dir", origin, "rev-list", "--count", "master"], text=True))

    def test_halt_before_first_publication_creates_no_recipe_or_ref(self):
        result = self.halt("1.0.0", "never-published")
        self.assertEqual(result, {channel: {"package": package, "availability": "no-recipe", "withdrawn": False}
                                  for channel, package in PACKAGES.items()})
        for publisher in self.origins.values():
            self.assertIsNone(publisher.head())
            self.assertEqual(list(Path(publisher.origin).joinpath("objects").glob("??/*")), [])
        self.assertEqual(list((self.root / "never-published").rglob("PKGBUILD")), [])

    def test_first_publication_repeat_advance_halt_and_recovery_preserve_channel_ownership(self):
        first = {channel: self.publish(channel, "1.0.0") for channel in PACKAGES}
        self.assertEqual({channel: self.count(channel) for channel in PACKAGES}, {"stable": 1, "preview": 1})
        advanced = self.publish("stable", "1.0.1")
        halted = self.halt("1.0.0", "halt-first")
        self.assertEqual(halted["stable"]["availability"], "other-version")
        preview = halted["preview"]
        self.assertTrue(preview["withdrawn"])
        self.assertEqual(preview["withdrawn_commit"], first["preview"]["commit"])
        self.assertEqual(preview["pkgrel"], 2)
        self.assertEqual(self.halt("1.0.0", "halt-repeat")["preview"]["commit"], preview["commit"])
        self.assertEqual(self.origins[PACKAGES["stable"]].head(), advanced["commit"])
        self.assertEqual({channel: self.count(channel) for channel in PACKAGES}, {"stable": 2, "preview": 2})
        origin = self.origins[PACKAGES["preview"]].origin
        srcinfo = subprocess.check_output(["git", "--git-dir", origin, "show", "master:.SRCINFO"], text=True)
        self.assertIn("pkgbase = " + PACKAGES["preview"], srcinfo)
        self.assertIn("\tpkgrel = 2\n", srcinfo)
        self.assertNotIn("source", srcinfo)
        self.assertNotIn("epoch", srcinfo)
        refusal = subprocess.check_output(["git", "--git-dir", origin, "show", "master:PKGBUILD"], text=True)
        self.assertIn("return 1", refusal)
        recovered = self.publish("preview", "1.0.1")
        record = json.loads(subprocess.check_output(
            ["git", "--git-dir", origin, "log", "-1", "--format=%B"], text=True))
        self.assertEqual(record["previous"], preview["commit"])
        self.assertEqual(recovered["channel"], "preview")
        self.assertEqual(self.count("preview"), 3)

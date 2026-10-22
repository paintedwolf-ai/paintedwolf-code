"""AUR recipes pin admitted native generations and refuse inputs that disagree before writing."""

import json
from pathlib import Path
import shutil
import tempfile
import unittest

from scripts.linux_release.aur_recipe import generate
from scripts.linux_release.repository_operations import identity
from scripts.verification_tests.distribution.pacman_repository_fixtures import (
    PACKAGE, generation, native_tools, trust)

ORIGIN = "https://repository.example.invalid"


class AurRecipeTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.catalog = trust(self.work / "trust")
        self.production = trust(self.work / "production", rehearsal=False)
        self.x86_64 = generation("pacman-0.0.1-x86_64", self.work)
        self.aarch64 = generation("pacman-0.0.1-aarch64", self.work)
        self.recipe = self.work / "recipe"

    def released(self, *selected):
        return [({**receipt, "rehearsal": False}, directory) for receipt, directory in selected]

    def refuses(self, reason, generations, rehearsal=True, base_url=ORIGIN, package=PACKAGE):
        with self.assertRaisesRegex(ValueError, reason):
            generate(generations, self.recipe, base_url, self.production if not rehearsal else self.catalog,
                     rehearsal, package)
        self.assertFalse(self.recipe.exists())

    def test_production_recipe_pins_both_architectures_signer_and_generation_sources(self):
        native_tools("makepkg", "bsdtar")
        selected = self.released(self.x86_64, self.aarch64)
        metadata = generate(selected, self.recipe, ORIGIN, self.production, False, PACKAGE)
        self.assertEqual(metadata, {"format_version": 1, "source_commit": "f" * 40, "version": "0.0.1",
                                    "channel": "preview", "package": PACKAGE + "-preview-bin", "rehearsal": False,
                                    "aur_published": False, "architectures": ["aarch64", "x86_64"]})
        recipe = (self.recipe / "PKGBUILD").read_text()
        fingerprint = json.loads(self.production.read_text())["generations"][0]["fingerprint"]
        self.assertIn(f"validpgpkeys=({fingerprint})", recipe)
        self.assertIn("arch=(aarch64 x86_64)", recipe)
        self.assertIn(f"provides=({PACKAGE}=0.0.1-1)", recipe)
        for receipt, _ in selected:
            [row] = receipt["packages"]
            source = f"{ORIGIN}/generations/{identity(receipt)}/{row['file']}"
            self.assertIn(f"source_{receipt['architecture']}=({source} {source}.sig)", recipe)
            self.assertIn(f"sha256sums_{receipt['architecture']}=({row['sha256']} SKIP)", recipe)
        self.assertIn(f"pkgname = {PACKAGE}-preview-bin", (self.recipe / ".SRCINFO").read_text())
        self.assertFalse((self.recipe / "painted-wolf-code.install").exists())

    def test_production_needs_both_architectures_and_production_trust(self):
        self.refuses("requires both supported architectures", self.released(self.x86_64), rehearsal=False)
        self.refuses("cross-trust", [self.x86_64, self.aarch64], rehearsal=False)
        self.refuses("cross-trust", self.released(self.x86_64, self.aarch64))
        with self.assertRaisesRegex(ValueError, "trust mode"):
            generate([self.x86_64], self.recipe, ORIGIN, self.catalog, False, PACKAGE)

    def test_architectures_must_share_one_release_identity_once_each(self):
        second = generation("pacman-0.0.2-x86_64", self.work)
        self.refuses("differ in release identity", [self.x86_64, second])
        copy = self.work / "copy"
        shutil.copytree(self.x86_64[1], copy)
        self.refuses("architecture is repeated", [self.x86_64, (self.x86_64[0], copy)])

    def test_inputs_must_be_admitted_selected_application_packages(self):
        package = self.x86_64[1] / self.x86_64[0]["packages"][0]["file"]
        package.write_bytes(package.read_bytes() + b"changed")
        self.refuses("selected final bytes", [self.x86_64])
        self.refuses("not the selected application package", [self.aarch64], package="painted-wolf-code")
        self.refuses("needs selected native generations", [])
        for url in ["http://repository.example.invalid", "https://user@repository.example.invalid",
                    "https://repository.example.invalid/?release=1"]:
            with self.subTest(url=url):
                self.refuses("HTTPS origin", [self.aarch64], base_url=url)


if __name__ == "__main__":
    unittest.main()

"""AUR recipes pin admitted native generations and delivery workflows collect bound inputs."""

import hashlib
import json
from pathlib import Path
import shutil
import tempfile
import unittest

from scripts.linux_release.aur_delivery_inputs import collect, final_inventory
from scripts.linux_release.aur_delivery_workflow import main, prepare
from scripts.linux_release.aur_recipe import generate
from scripts.linux_release.repository_objects import encoded
from scripts.linux_release.repository_operations import identity, transition
from scripts.verification_tests.distribution.pacman_repository_fixtures import (
    PACKAGE, TESTDATA, generation, native_tools, trust)

SOURCE = "f" * 40
RECIPE_ORIGIN = "https://repository.example.invalid"
DELIVERY_ORIGIN = "https://repository.example.invalid/linux/pacman"


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

    def refuses(self, reason, generations, rehearsal=True, base_url=RECIPE_ORIGIN, package=PACKAGE):
        with self.assertRaisesRegex(ValueError, reason):
            generate(generations, self.recipe, base_url, self.production if not rehearsal else self.catalog,
                     rehearsal, package)
        self.assertFalse(self.recipe.exists())

    def test_production_recipe_pins_both_architectures_signer_and_generation_sources(self):
        native_tools("makepkg", "bsdtar")
        selected = self.released(self.x86_64, self.aarch64)
        metadata = generate(selected, self.recipe, RECIPE_ORIGIN, self.production, False, PACKAGE)
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
            source = f"{RECIPE_ORIGIN}/generations/{identity(receipt)}/{row['file']}"
            self.assertIn(f"source_{receipt['architecture']}=({source} {source}.sig)", recipe)
            self.assertIn(f"sha256sums_{receipt['architecture']}=({row['sha256']} SKIP)", recipe)
        self.assertIn(f"pkgname = {PACKAGE}-preview-bin", (self.recipe / ".SRCINFO").read_text())
        self.assertFalse((self.recipe / "painted-wolf-code.install").exists())

    def test_production_needs_both_architectures_and_production_trust(self):
        self.refuses("requires both supported architectures", self.released(self.x86_64), rehearsal=False)
        self.refuses("cross-trust", [self.x86_64, self.aarch64], rehearsal=False)
        self.refuses("cross-trust", self.released(self.x86_64, self.aarch64))
        with self.assertRaisesRegex(ValueError, "trust mode"):
            generate([self.x86_64], self.recipe, RECIPE_ORIGIN, self.catalog, False, PACKAGE)

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


class PublishedRepository:
    """The public repository layout `pacman_publication` writes, served from a local store."""

    def __init__(self, root: Path):
        self.root = root

    def publish(self, name: str, **changes) -> dict:
        manifest = {**json.loads((TESTDATA / name / "generation.json").read_text()), "rehearsal": False, **changes}
        generation_dir = self.root / "generations" / identity(manifest)
        generation_dir.mkdir(parents=True, exist_ok=True)
        for row in manifest["objects"]:
            if (TESTDATA / name / row["path"]).is_file():
                shutil.copyfile(TESTDATA / name / row["path"], generation_dir / row["path"])
        (generation_dir / "generation.json").write_bytes(encoded(manifest))
        self.activate(manifest, transition(manifest, None, None))
        return manifest

    def activate(self, manifest: dict, record: dict):
        channel = self.root / "channels/pacman" / manifest["channel"] / manifest["architecture"]
        channel.mkdir(parents=True, exist_ok=True)
        (channel / "current.json").write_bytes(encoded(record))

    def fetch(self, url: str, destination: Path, limit: int):
        if not url.startswith(DELIVERY_ORIGIN + "/"):
            raise ValueError("fixture repository serves only its own origin")
        source = self.root / url.removeprefix(DELIVERY_ORIGIN + "/")
        if source.stat().st_size > limit:
            raise ValueError("published repository object exceeds its selected size")
        with destination.open("xb") as output:
            output.write(source.read_bytes())


class AurDeliveryInputTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.repository = PublishedRepository(self.work / "origin")
        self.final = self.work / "final-artifacts"
        self.final.mkdir()
        (self.final / "painted-wolf-code_v0.0.1_darwin-aarch64.dmg").write_bytes(b"final bytes")
        self.output = self.work / "aur-inputs"

    def collect(self, channel="preview", source=SOURCE, origin=DELIVERY_ORIGIN):
        return collect(origin, channel, source, self.final, self.output, fetch=self.repository.fetch)

    def test_bundle_holds_each_active_generation_and_the_controller_final_inventory(self):
        manifests = [self.repository.publish("pacman-0.0.1-x86_64"), self.repository.publish("pacman-0.0.1-aarch64")]
        delivery = self.collect()
        self.assertEqual(delivery, {"format_version": 1, "source_commit": SOURCE, "base_url": DELIVERY_ORIGIN,
                                    "generations": ["pacman-x86_64", "pacman-aarch64"]})
        self.assertEqual(sorted(path.name for path in self.output.iterdir()),
                         ["delivery.json", "final-artifacts.json", "pacman-aarch64", "pacman-x86_64"])
        for name, manifest in zip(delivery["generations"], manifests):
            retained = json.loads((self.output / name / "generation.json").read_text())
            self.assertEqual(identity(retained), identity(manifest))
            for row in manifest["objects"]:
                raw = (self.output / name / row["path"]).read_bytes()
                self.assertEqual(hashlib.sha256(raw).hexdigest(), row["sha256"])
        inventory = json.loads((self.output / "final-artifacts.json").read_text())
        self.assertEqual(inventory["source_commit"], SOURCE)
        self.assertEqual(inventory["artifacts"], [{"path": "painted-wolf-code_v0.0.1_darwin-aarch64.dmg",
                                                   "sha256": hashlib.sha256(b"final bytes").hexdigest(), "size": 11}])

    def test_active_generation_for_another_source_commit_is_refused(self):
        self.repository.publish("pacman-0.0.1-x86_64", source_commit="e" * 40)
        self.repository.publish("pacman-0.0.1-aarch64")
        with self.assertRaisesRegex(ValueError, "differs from the selected source commit"):
            self.collect()
        shutil.rmtree(self.output)
        self.repository.publish("pacman-0.0.1-x86_64")
        with self.assertRaisesRegex(ValueError, "selected source commit"):
            self.collect(source="e" * 40)

    def test_channel_state_must_select_the_exact_retained_production_generation(self):
        manifest = self.repository.publish("pacman-0.0.1-x86_64")
        self.repository.publish("pacman-0.0.1-aarch64")
        record = transition(manifest, None, None)
        for change, reason in [({"rehearsal": True}, "trust mode"), ({"operation": "withdraw"}, "unsupported shape"),
                               ({"generation": "0" * 64}, ""), ({"version": "0.0.9"}, "active channel facts")]:
            with self.subTest(change=change):
                self.repository.activate(manifest, {**record, **change})
                with self.assertRaisesRegex((ValueError, OSError), reason):
                    self.collect()
                shutil.rmtree(self.output, ignore_errors=True)
        self.repository.activate(manifest, record)
        generation_dir = self.repository.root / "generations" / identity(manifest)
        (generation_dir / "generation.json").write_bytes(encoded({**manifest, "version": "0.0.9"}))
        with self.assertRaisesRegex(ValueError, "differs from its active identity"):
            self.collect()

    def test_published_objects_must_match_their_generation_inventory(self):
        manifest = self.repository.publish("pacman-0.0.1-x86_64")
        self.repository.publish("pacman-0.0.1-aarch64")
        package = next(row["path"] for row in manifest["objects"] if row["path"].endswith(".pkg.tar.zst"))
        path = self.repository.root / "generations" / identity(manifest) / package
        original = path.read_bytes()
        path.write_bytes(original[:-1] + bytes([original[-1] ^ 1]))
        with self.assertRaisesRegex(ValueError, "differs from its generation inventory"):
            self.collect()
        shutil.rmtree(self.output)
        path.write_bytes(original + b"appended")
        with self.assertRaisesRegex(ValueError, "exceeds its selected size"):
            self.collect()
        shutil.rmtree(self.output)
        escaping = {**manifest, "objects": [{**manifest["objects"][0], "path": "../current.json"}]}
        retained = self.repository.root / "generations" / identity(escaping)
        retained.mkdir()
        (retained / "generation.json").write_bytes(encoded(escaping))
        self.repository.activate(escaping, transition(escaping, None, None))
        with self.assertRaisesRegex(ValueError, "sibling file"):
            self.collect()

    def test_architectures_selection_and_final_files_are_closed(self):
        self.repository.publish("pacman-0.0.2-x86_64")
        with self.assertRaises(FileNotFoundError):
            self.collect()
        shutil.rmtree(self.output)
        self.repository.publish("pacman-0.0.1-aarch64")
        with self.assertRaisesRegex(ValueError, "differ in release version"):
            self.collect()
        self.assertFalse((self.output / "delivery.json").exists())
        shutil.rmtree(self.output)
        for channel, source, origin, reason in [("nightly", SOURCE, DELIVERY_ORIGIN, "stable or preview"),
                                                ("preview", "HEAD", DELIVERY_ORIGIN, "exact release source"),
                                                ("preview", SOURCE, "http://repository.example.invalid", "HTTPS"),
                                                ("preview", SOURCE, "https://user@repository.example.invalid", "HTTPS")]:
            with self.subTest(channel=channel, source=source, origin=origin), self.assertRaisesRegex(ValueError, reason):
                self.collect(channel, source, origin)
        empty = self.work / "empty"
        empty.mkdir()
        with self.assertRaisesRegex(ValueError, "retained no final artifacts"):
            final_inventory(empty, SOURCE)
        (self.final / "linked").symlink_to(self.final / "painted-wolf-code_v0.0.1_darwin-aarch64.dmg")
        with self.assertRaisesRegex(ValueError, "regular files"):
            final_inventory(self.final, SOURCE)
        self.assertFalse(self.output.exists())
        try:
            main(["collect", "--repository-url", "http://repository.example.invalid", "--channel", "preview",
                  "--source-commit", SOURCE, "--final-artifacts", str(self.final), "--output", str(self.output)])
        except SystemExit as stopped:
            self.assertEqual(stopped.code, 1)
        else:
            self.fail("collect accepted a plain HTTP repository origin")
        self.assertFalse(self.output.exists())

    def test_collected_bundle_prepares_the_bound_recipe(self):
        native_tools("makepkg", "bsdtar")
        self.repository.publish("pacman-0.0.1-x86_64")
        self.repository.publish("pacman-0.0.1-aarch64")
        self.collect()
        catalog = trust(self.work / "trust", rehearsal=False)
        receipt = self.work / "output/delivery.json"
        selected = prepare(self.output, SOURCE, receipt, "prepare", catalog=catalog, package=PACKAGE)
        self.assertEqual(selected["final_artifacts_sha256"],
                         hashlib.sha256((self.output / "final-artifacts.json").read_bytes()).hexdigest())
        self.assertEqual({row["architecture"] for row in selected["generations"]}, {"x86_64", "aarch64"})
        pkgbuild = (receipt.parent / "recipe/PKGBUILD").read_text()
        self.assertIn(DELIVERY_ORIGIN + "/generations/", pkgbuild)


if __name__ == "__main__":
    unittest.main()

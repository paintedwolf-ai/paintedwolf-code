"""The AUR delivery workflow collects its input bundle from published generations bound to one source."""

import hashlib
import json
from pathlib import Path
import shutil
import tempfile
import unittest

from scripts.linux_release.aur_delivery_inputs import collect, final_inventory
from scripts.linux_release.aur_delivery_workflow import main, prepare
from scripts.linux_release.repository_objects import encoded
from scripts.linux_release.repository_operations import identity, transition
from scripts.verification_tests.distribution.pacman_repository_fixtures import PACKAGE, TESTDATA, native_tools, trust

SOURCE = "f" * 40
ORIGIN = "https://repository.example.invalid/linux/pacman"


class PublishedRepository:
    """The public repository layout `pacman_publication` writes, served from a local store."""

    def __init__(self, root: Path):
        self.root = root

    def publish(self, name: str, **changes) -> dict:
        manifest = {**json.loads((TESTDATA / name / "generation.json").read_text()), "rehearsal": False, **changes}
        generation = self.root / "generations" / identity(manifest)
        generation.mkdir(parents=True, exist_ok=True)
        for row in manifest["objects"]:
            if (TESTDATA / name / row["path"]).is_file():
                shutil.copyfile(TESTDATA / name / row["path"], generation / row["path"])
        (generation / "generation.json").write_bytes(encoded(manifest))
        self.activate(manifest, transition(manifest, None, None))
        return manifest

    def activate(self, manifest: dict, record: dict):
        channel = self.root / "channels/pacman" / manifest["channel"] / manifest["architecture"]
        channel.mkdir(parents=True, exist_ok=True)
        (channel / "current.json").write_bytes(encoded(record))

    def fetch(self, url: str, destination: Path, limit: int):
        if not url.startswith(ORIGIN + "/"):
            raise ValueError("fixture repository serves only its own origin")
        source = self.root / url.removeprefix(ORIGIN + "/")
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

    def collect(self, channel="preview", source=SOURCE, origin=ORIGIN):
        return collect(origin, channel, source, self.final, self.output, fetch=self.repository.fetch)

    def test_bundle_holds_each_active_generation_and_the_controller_final_inventory(self):
        manifests = [self.repository.publish("pacman-0.0.1-x86_64"), self.repository.publish("pacman-0.0.1-aarch64")]
        delivery = self.collect()
        self.assertEqual(delivery, {"format_version": 1, "source_commit": SOURCE, "base_url": ORIGIN,
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
        generation = self.repository.root / "generations" / identity(manifest)
        (generation / "generation.json").write_bytes(encoded({**manifest, "version": "0.0.9"}))
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
        for channel, source, origin, reason in [("nightly", SOURCE, ORIGIN, "stable or preview"),
                                                ("preview", "HEAD", ORIGIN, "exact release source"),
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
        self.assertIn(ORIGIN + "/generations/", pkgbuild)


if __name__ == "__main__":
    unittest.main()

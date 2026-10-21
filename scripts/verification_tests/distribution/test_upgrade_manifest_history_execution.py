"""Historical sources retain their published platform set as the catalog grows."""
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

from update_keys import load_registry, release_binding
from updater_signature import rehearsal_signature

ROOT = Path(__file__).resolve().parents[3]
ORIGIN = "https://downloads.paintedwolf.dev"


class ManifestHistoryTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        (self.root / "scripts").mkdir()
        (self.root / "packaging").mkdir()
        for name in ("release-validate-updater-manifest.sh", "semver-compare.py", "release_semver.py", "update_keys.py", "updater_signature.py"):
            shutil.copyfile(ROOT / "scripts" / name, self.root / "scripts" / name)
        shutil.copyfile(ROOT / "packaging/update-keys.json", self.root / "packaging/update-keys.json")
        catalog = json.loads((ROOT / "packaging/release-platforms.json").read_text())
        for row in catalog["platforms"]:
            if row["updater_key"] == "windows-x86_64":
                row["publication"] = "public"
        (self.root / "packaging/release-platforms.json").write_text(json.dumps(catalog))
        keys = load_registry()
        keys["signing_generation"] = keys["embedded_generation"] = len(keys["generations"])
        version = "1.0.2"
        self.manifest = {"version": version, "notes": "Retained source", "pub_date": "2026-01-01T00:00:00Z",
                         "update_keys": release_binding(keys, version),
                         "platforms": {"darwin-aarch64": {"signature": rehearsal_signature(version),
                             "url": ORIGIN + f"/releases/v{version}/painted-wolf-code_v{version}_darwin-aarch64.app.tar.gz"}}}

    def validate(self, existing):
        import os
        path = self.root / "manifest.json"
        path.write_text(json.dumps(self.manifest))
        arguments = ["bash", str(self.root / "scripts/release-validate-updater-manifest.sh"), "--file", str(path)]
        if existing:
            arguments.append("--existing")
        return subprocess.run(arguments, env={**os.environ, "DOWNLOAD_BASE_URL": ORIGIN}, capture_output=True, text=True, timeout=30)

    def test_published_sources_keep_their_original_platforms_but_new_offers_require_every_public_platform(self):
        self.assertEqual(self.validate(True).returncode, 0)
        self.assertNotEqual(self.validate(False).returncode, 0)

    def test_empty_and_unknown_historical_platform_sets_are_refused(self):
        self.manifest["platforms"] = {}
        self.assertNotEqual(self.validate(True).returncode, 0)
        self.manifest["platforms"] = {"unknown-aarch64": {"signature": "signature", "url": ORIGIN}}
        self.assertNotEqual(self.validate(True).returncode, 0)

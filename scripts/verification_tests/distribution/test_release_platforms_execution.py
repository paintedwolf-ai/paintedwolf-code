"""Public packages and automatic updater artifacts have separate catalog owners."""
import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import release_distribution as distribution
from release_distribution_contract.platforms import load_platforms, package_urls
from update_keys import load_registry, release_binding
from updater_signature import rehearsal_signature


ROOT = Path(__file__).resolve().parents[3]
VERSION = "1.0.0-rc.1"
ROWS = [
    {"updater_key": "darwin-aarch64", "publication": "public", "package_extensions": ["dmg"], "updater_extension": "app.tar.gz"},
    {"updater_key": "linux-x86_64", "publication": "public", "package_extensions": ["deb", "rpm"], "updater_extension": None},
    {"updater_key": "linux-aarch64", "publication": "public", "package_extensions": ["deb", "rpm"], "updater_extension": None},
    {"updater_key": "windows-x86_64", "publication": "candidate", "package_extensions": ["exe"], "updater_extension": "exe"},
]


class PublicPlatformTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.work = Path(self.temporary.name)
        self.catalog = self.work / "platforms.json"
        self.write_catalog(ROWS)

    def write_catalog(self, rows):
        self.catalog.write_text(json.dumps({"schema_version": 2, "platforms": rows}))

    def run_script(self, script, *args, success=True):
        command = ["bash" if script.endswith(".sh") else sys.executable, str(ROOT / "scripts" / script), *map(str, args)]
        result = subprocess.run(command, capture_output=True, text=True, timeout=30,
                                env={**os.environ, "DOWNLOAD_BASE_URL": "https://downloads.paintedwolf.dev"})
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        return result

    def test_website_requires_every_public_package_in_any_display_order(self):
        expected = {"channel": "stable", "version": VERSION}
        urls = package_urls(VERSION, self.catalog)
        self.assertEqual(len(urls), 5)
        self.assertFalse(any(".exe" in url or "app.tar.gz" in url for url in urls))
        def page(links):
            return "".join(f'<a data-release-channel="stable" download href="{url}">Download</a>' for url in links)
        distribution.validate_website_downloads(page(reversed(urls)), expected, catalog=self.catalog)
        for links in [urls[:-1], urls + [urls[0]], urls + [urls[0].replace("linux-aarch64", "windows-x86_64")],
                      [url.replace(VERSION, "0.9.0") for url in urls], [url.replace("https://", "http://") for url in urls]]:
            with self.subTest(links=links), self.assertRaises(ValueError):
                distribution.validate_website_downloads(page(links), expected, catalog=self.catalog)
        distribution.validate_website_downloads("", {**expected, "version": None}, catalog=self.catalog)
        with self.assertRaises(ValueError):
            distribution.validate_website_downloads(page(urls), {**expected, "version": None}, catalog=self.catalog)

    def test_catalog_selection_does_not_promote_candidate_packages(self):
        candidate_rows = [{**row, "publication": "candidate"} if row["updater_key"].startswith("linux-") else row for row in ROWS]
        self.write_catalog(candidate_rows)
        urls = package_urls(VERSION, self.catalog)
        self.assertEqual(len(urls), 1)
        self.assertTrue(urls[0].endswith("darwin-aarch64.dmg"))
        self.write_catalog(ROWS)
        self.assertEqual(len(package_urls(VERSION, self.catalog)), 5)

    def test_catalog_rejects_missing_ownership_repeats_and_unsafe_names(self):
        invalid = []
        for field, value in [("updater_key", "../../linux"), ("publication", "ready"),
                             ("package_extensions", []), ("package_extensions", ["deb", "deb"]),
                             ("package_extensions", ["../deb"]), ("updater_extension", "")]:
            rows = copy.deepcopy(ROWS)
            rows[0][field] = value
            invalid.append(rows)
        rows = copy.deepcopy(ROWS)
        del rows[1]["updater_extension"]
        invalid.extend([rows, ROWS + [ROWS[0]], [{**row, "publication": "candidate"} for row in ROWS]])
        for rows in invalid:
            with self.subTest(rows=rows):
                self.write_catalog(rows)
                with self.assertRaises(ValueError):
                    load_platforms(self.catalog)

    def stage_inputs(self):
        source = self.work / "input"
        (source / "opengrep").mkdir(parents=True)
        expected = set()
        for row in ROWS:
            stem = f"painted-wolf-code_v{VERSION}_{row['updater_key']}"
            names = [f"{stem}.{ext}{suffix}" for ext in row["package_extensions"] for suffix in ("", ".sha256")]
            if row["updater_extension"] is not None:
                names += [f"{stem}.{row['updater_extension']}", f"{stem}.{row['updater_extension']}.sig"]
            for name in names:
                (source / name).write_bytes(name.encode())
            (source / "opengrep" / f"{stem}.json").write_text("{}")
            if row["publication"] == "public":
                expected.update(names + [f"{stem}.opengrep.json"])
        return source, expected

    def test_collector_requires_linux_packages_checksums_and_audits_without_updaters(self):
        source, expected = self.stage_inputs()
        output = self.work / "public"
        self.run_script("release-collect-public-artifacts.py", "--catalog", self.catalog,
                        "--input", source, "--output", output, "--version", VERSION)
        self.assertEqual({path.name for path in output.iterdir()}, expected)
        for index, relative in enumerate([
            f"painted-wolf-code_v{VERSION}_linux-x86_64.rpm",
            f"painted-wolf-code_v{VERSION}_linux-aarch64.deb.sha256",
            f"opengrep/painted-wolf-code_v{VERSION}_linux-aarch64.json",
            f"painted-wolf-code_v{VERSION}_darwin-aarch64.app.tar.gz.sig",
        ]):
            path = source / relative
            content = path.read_bytes()
            path.unlink()
            with self.subTest(missing=relative):
                self.run_script("release-collect-public-artifacts.py", "--catalog", self.catalog,
                                "--input", source, "--output", self.work / f"missing-{index}", "--version", VERSION, success=False)
            target = self.work / f"outside-{index}"
            target.write_bytes(content)
            path.symlink_to(target)
            with self.subTest(symlink=relative):
                self.run_script("release-collect-public-artifacts.py", "--catalog", self.catalog,
                                "--input", source, "--output", self.work / f"symlink-{index}", "--version", VERSION, success=False)
            path.unlink()
            path.write_bytes(content)

    def test_assembler_and_validator_exclude_package_owned_updaters(self):
        fragments = self.work / "fragments"
        fragments.mkdir()
        binding = release_binding(load_registry(), VERSION)
        fragment = {"platform": "darwin-aarch64", "signature": rehearsal_signature(VERSION),
                    "updater_artifact": f"painted-wolf-code_v{VERSION}_darwin-aarch64.app.tar.gz", "update_keys": binding}
        (fragments / "darwin.json").write_text(json.dumps(fragment))
        notes, output = self.work / "notes", self.work / "manifest.json"
        notes.write_text("A fixture release.")
        args = ["--catalog", self.catalog, "--fragments", fragments, "--version", VERSION, "--notes", notes,
                "--pub-date", "2026-10-18T00:00:00Z", "--base-url", "https://downloads.paintedwolf.dev", "--output", output]
        self.run_script("release-assemble-updater-manifest.py", *args)
        manifest = json.loads(output.read_text())
        self.assertEqual(set(manifest["platforms"]), {"darwin-aarch64"})
        self.run_script("release-validate-updater-manifest.sh", "--file", output, "--catalog", self.catalog)
        manifest["platforms"]["linux-x86_64"] = manifest["platforms"]["darwin-aarch64"]
        output.write_text(json.dumps(manifest))
        self.run_script("release-validate-updater-manifest.sh", "--file", output, "--catalog", self.catalog, success=False)
        (fragments / "linux.json").write_text(json.dumps({**fragment, "platform": "linux-x86_64"}))
        self.run_script("release-assemble-updater-manifest.py", *args, success=False)
        (fragments / "linux.json").unlink()
        (fragments / "darwin.json").unlink()
        self.run_script("release-assemble-updater-manifest.py", *args, success=False)

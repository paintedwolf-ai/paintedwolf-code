"""Linux candidates use catalog identities without inheriting publication authority."""

import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest

from linux_release.candidates import matrix, verify

ROOT = Path(__file__).resolve().parents[3]


class LinuxCandidateTests(unittest.TestCase):
    def test_pacman_family_is_declared_without_changing_native_runner_identity(self):
        catalog = copy.deepcopy(self.catalog)
        for row in catalog["platforms"]:
            if row.get("goos") == "linux":
                row["package_extensions"] = ["deb", "rpm", "pkg.tar.zst"]
        result = matrix(catalog)
        self.assertEqual(len(result["include"]), 2)
        self.assertTrue(all(row["package_extensions"] == ["deb", "rpm", "pkg.tar.zst"] for row in result["include"]))

    def setUp(self):
        self.catalog = json.loads((ROOT / "packaging/release-platforms.json").read_text())

    def test_matrix_uses_both_catalog_architectures_and_no_other_platform(self):
        rows = matrix(self.catalog)["include"]
        self.assertEqual({row["goarch"] for row in rows}, {"amd64", "arm64"})
        self.assertTrue(all(row["goos"] == "linux" for row in rows))
        for row in rows:
            self.assertEqual(verify(self.catalog, row["updater_key"], row["runner"], row["goarch"]), row)

    def test_missing_duplicate_or_changed_native_contract_fails(self):
        for field, value in [("runner", "wrong"), ("goarch", "386"), ("artifact_arch", "armhf"),
                             ("goos", "darwin"), ("publication", "unknown"),
                             ("package_extensions", ["deb"]), ("updater_extension", "app.tar.gz")]:
            catalog = copy.deepcopy(self.catalog)
            row = next(row for row in catalog["platforms"] if row["goarch"] == "amd64")
            row[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                verify(catalog, "linux-x86_64", "ubuntu-24.04", "amd64")
        for rows in ([], [self.catalog["platforms"][1]] * 2):
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                matrix({"schema_version": 2, "platforms": rows})

    def test_runner_identity_variable_does_not_replace_the_catalog_label(self):
        command = [sys.executable, str(ROOT / "scripts/linux_release/candidates.py"),
                   "--catalog", str(ROOT / "packaging/release-platforms.json"),
                   "--platform", "linux-x86_64", "--runner-label", "ubuntu-24.04",
                   "--architecture", "amd64"]
        environment = {**os.environ, "RUNNER_NAME": "GitHub Actions 1000000000"}
        result = subprocess.run(command, env=environment, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["runner"], "ubuntu-24.04")
        command[command.index("--runner-label") + 1] = "different-label"
        result = subprocess.run(command, env=environment, capture_output=True, text=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")

    def test_public_linux_rows_still_have_nonpublishing_candidate_builds(self):
        for row in self.catalog["platforms"]:
            if row["goos"] == "linux":
                row["publication"] = "public"
        self.assertEqual(len(matrix(self.catalog)["include"]), 2)

"""Qualification rows retain each installation, architecture and native obligation."""

import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from linux_release.support_matrix import CATALOG, expand


class LinuxSupportMatrixTests(unittest.TestCase):
    def setUp(self):
        self.catalog = json.loads(CATALOG.read_text(encoding="utf-8"))

    def test_rows_cover_each_declared_combination_with_stable_identity(self):
        rows = expand(self.catalog)
        expected = {
            "/".join((environment["id"], installation, architecture, capability["id"]))
            for environment in self.catalog["environments"]
            for installation in environment["installations"]
            for architecture in environment["architectures"]
            for capability in self.catalog["capabilities"]
        }
        self.assertEqual({row["id"] for row in rows}, expected)
        self.assertEqual(len(rows), len(expected))
        reordered = copy.deepcopy(self.catalog)
        for field in ("architectures", "installations", "capabilities", "environments"):
            reordered[field].reverse()
        self.assertEqual(expand(reordered), rows)
        for row in rows:
            self.assertTrue(row["issues"])
            self.assertTrue(row["cases"])
            self.assertEqual(row["emulation_allowed"], row["tier"] == "installed-functional")

    def test_native_arch_is_distinct_from_nix_and_does_not_invent_artix_arm(self):
        rows = expand(self.catalog)
        native_arm = [row for row in rows if row["architecture"] == "arm64"
                      and row["installation"] == "pacman"]
        self.assertTrue(any(row["environment"] == "arch-arm-plasma" for row in native_arm))
        self.assertFalse(any(row["environment"].startswith("artix") for row in native_arm))
        nix = {row["installation"] for row in rows if row["environment"].startswith("nixos")}
        self.assertEqual(nix, {"nixos-system", "nix-package", "nix-profile", "home-manager", "nix-run"})
        self.assertFalse(any(row["emulation_allowed"] for row in rows
                             if row["tier"].startswith("native-")))

    def test_invalid_catalogs_fail_instead_of_dropping_obligations(self):
        mutations = [
            lambda c: c.update(format_version=2),
            lambda c: c["environments"][0].update(architectures=["unknown"]),
            lambda c: c["environments"][0].update(installations=["deb", "deb"]),
            lambda c: c["capabilities"][0].update(tier="fixture"),
            lambda c: c["capabilities"][0].update(issues=[]),
            lambda c: c["capabilities"][0].update(cases=[]),
            lambda c: c["environments"][0].update(portal=""),
            lambda c: c["architectures"].append({"id": "uncovered", "nix_system": "uncovered-linux"}),
            lambda c: c["installations"].append(copy.deepcopy(c["installations"][0])),
        ]
        for mutation in mutations:
            catalog = copy.deepcopy(self.catalog)
            mutation(catalog)
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                expand(catalog)

    def test_command_emits_rows_and_rejects_unknown_catalog_version(self):
        script = Path(__file__).resolve().parents[2] / "linux_release" / "support_matrix.py"
        command = [sys.executable, "-m", "scripts.linux_release.support_matrix"]
        result = subprocess.run(command, cwd=script.parents[2], capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["rows"], expand(self.catalog))
        with tempfile.TemporaryDirectory() as directory:
            bad = Path(directory) / "unsupported.json"
            bad.write_text('{"format_version": 0}', encoding="utf-8")
            result = subprocess.run([*command, "--catalog", str(bad)], cwd=script.parents[2],
                                    capture_output=True, text=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        self.assertIn("unsupported", result.stderr)

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

    def test_artix_retains_openrc_and_both_maintained_protocol_classes(self):
        environments = {row["id"]: row for row in self.catalog["environments"]}
        self.assertNotIn("artix-openrc", environments)
        selected = {"artix-openrc-plasma-wayland": ("Plasma", "Wayland", "KDE"),
                    "artix-openrc-xfce-x11": ("Xfce", "X11", "GTK")}
        rows = expand(self.catalog)
        capabilities = {row["id"]: row for row in self.catalog["capabilities"]}
        expected_modes = {"pacman", "nix-package", "nix-profile", "home-manager", "nix-run"}
        for identifier, (desktop, protocol, portal) in selected.items():
            environment = environments[identifier]
            self.assertEqual((desktop, protocol, portal),
                             tuple(environment[key] for key in ("desktop", "display", "portal")))
            self.assertEqual(environment["architectures"], ["amd64"])
            self.assertEqual(set(environment["installations"]), expected_modes)
            self.assertEqual(environment["session"], "OpenRC with elogind")
            self.assertEqual(environment["security"], "protected OpenRC vault service state")
            self.assertIn("100%, 150% and 200%", environment["scaling"])
            self.assertIn("mixed-scale", environment["scaling"])
            admitted = [row for row in rows if row["environment"] == identifier]
            self.assertEqual({(row["installation"], row["capability"]) for row in admitted},
                             {(mode, capability) for mode in expected_modes for capability in capabilities})
            for row in admitted:
                self.assertEqual(row["cases"], capabilities[row["capability"]]["cases"])
                self.assertEqual(row["tier"], capabilities[row["capability"]]["tier"])
                self.assertEqual(row["emulation_allowed"], row["tier"] == "installed-functional")

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
        result = subprocess.run([sys.executable, str(script)], capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["rows"], expand(self.catalog))
        with tempfile.TemporaryDirectory() as directory:
            bad = Path(directory) / "unsupported.json"
            bad.write_text('{"format_version": 0}', encoding="utf-8")
            result = subprocess.run([sys.executable, str(script), "--catalog", str(bad)],
                                    capture_output=True, text=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        self.assertIn("unsupported", result.stderr)

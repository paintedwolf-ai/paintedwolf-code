"""Execute the catalog's scanner recipe against disposable prerequisite fixtures."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

import verification_plan as planning

SCRIPTS = Path(__file__).resolve().parents[1]
IDENTITY = "eyJmaXh0dXJlIjoiYXV0aGVudGljYXRlZC1lbmdpbmUifQ=="


class ScannerQualificationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="scanner-qualification-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.scripts = self.root / "scripts"
        self.scripts.mkdir()
        shutil.copy2(SCRIPTS / "with-bundled-scanners.sh", self.scripts)
        # These are fixture dependencies, never the enclosing queue or live engine.
        (self.scripts / "test-execution.py").write_text("import sys\nassert sys.argv[1:] == ['holding']\n")
        (self.scripts / "resolve-opengrep.sh").write_text('''#!/bin/sh
case "$1" in
 --dir-only) [ "$FIXTURE_MISSING" != directory ] || exit 41; printf '%s\\n' "$FIXTURE_ENGINE_DIR" ;;
 --identity-only) [ "$FIXTURE_MISSING" != identity ] || exit 42; printf '%s\\n' "$FIXTURE_IDENTITY" ;;
 *) exit 43 ;;
esac
''')
        self.result = self.root / "invocation.json"
        (self.scripts / "go-test-digest.sh").write_text('''#!/bin/sh
exec python3 scripts/capture.py "$@"
''')
        (self.scripts / "capture.py").write_text('''import json, os, pathlib, sys
pathlib.Path(os.environ['FIXTURE_RESULT']).write_text(json.dumps({
 'arguments': sys.argv[1:], 'goflags': os.environ.get('GOFLAGS'),
 'required': os.environ.get('LYCAON_SCANNER_SUITE_REQUIRED'),
 'engine_root': os.environ.get('LYCAON_ENGINE_ROOT')}))
''')
        self.env = {**os.environ, "FIXTURE_ENGINE_DIR": str(self.root / "engine/bin/opengrep"),
                    "FIXTURE_IDENTITY": IDENTITY, "FIXTURE_RESULT": str(self.result),
                    "FIXTURE_MISSING": "", "GOFLAGS": "-mod=readonly"}

    def invoke(self, command, missing=""):
        return subprocess.run(command, cwd=self.root,
                              env={**self.env, "FIXTURE_MISSING": missing},
                              text=True, capture_output=True, timeout=10)

    def test_full_catalog_recipe_carries_authenticated_identity_and_requires_scanners(self):
        stage = next(s for s in planning.expand(["test:full"]) if s["kind"] == "go")
        self.assertTrue(stage["bundled"])
        command = planning.digest_command(stage, ["fixture/scanners"])
        result = self.invoke(command)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        invocation = json.loads(self.result.read_text())
        self.assertEqual(invocation["required"], "1")
        self.assertIn("-mod=readonly", invocation["goflags"])
        self.assertIn("-ldflags=-X=github.com/lycaon/lycaon/internal/scan/bundled.buildIdentityBase64=" + IDENTITY,
                      invocation["goflags"])
        self.assertEqual(invocation["engine_root"], str(self.root / "engine"))
        self.assertIn("--full", invocation["arguments"])
        self.assertIn("integration", invocation["arguments"])
        self.assertIn("fixture/scanners", invocation["arguments"])

    def test_missing_engine_or_identity_fails_before_any_scanner_test_runs(self):
        stage = planning.expand(["test:full"])[0]
        command = planning.digest_command(stage, ["fixture/scanners"])
        for missing, status in [("directory", 41), ("identity", 42)]:
            with self.subTest(prerequisite=missing):
                result = self.invoke(command, missing)
                self.assertEqual(result.returncode, status, result.stdout + result.stderr)
                self.assertFalse(self.result.exists(), "unqualified scanner tests ran")

    def test_required_hosted_profiles_cannot_select_an_unbundled_full_recipe(self):
        catalog = planning.catalog()
        for profile in ("check", "nightly", "release"):
            lanes = [lane for lane in catalog["ci"].values() if profile in lane["profiles"]]
            scanner_targets = [target for lane in lanes for target in lane["targets"] if target == "test:full"]
            self.assertTrue(scanner_targets, profile)
            for target in scanner_targets:
                stages = planning.expand([target])
                self.assertTrue(all(s["bundled"] for s in stages if s["kind"] == "go"), profile)

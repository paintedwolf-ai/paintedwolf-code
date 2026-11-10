"""Isolated sidecars consume the browser artifact prepared for their run."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class BrowserArtifactTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.state = self.root / "state"
        runtime = self.state / "runtime"
        runtime.mkdir(parents=True)
        sidecar = runtime / "sidecar"
        sidecar.write_text('#!/usr/bin/env python3\nimport json,os\nprint(json.dumps({"browser":os.environ["LYCAON_BROWSER_BIN"],"config":os.environ["LYCAON_CONFIG_DIR"]}))\n')
        sidecar.chmod(0o700)
        self.browser = self.make_browser("shared browser")
        (runtime / "browser.json").write_text(json.dumps({"path": str(self.browser), "source": "managed"}))
        source = self.root / "source"
        source.mkdir()
        self.env = dict(os.environ, LYCAON_E2E_STATE_DIR=str(self.state), LYCAON_CONFIG_DIR=str(source),
                        LYCAON_HARNESS_MODE="real", LYCAON_LLM_MOCK="1", LYCAON_BROWSER_BIN="")
        self.script = Path(__file__).resolve().parents[2] / "e2e-sidecar-serve.sh"

    def make_browser(self, name):
        directory = self.root / name
        directory.mkdir()
        browser = directory / "chrome-headless-shell"
        browser.write_text("fixture")
        browser.chmod(0o700)
        (directory / "icudtl.dat").write_text("fixture")
        return browser

    def run_sidecar(self):
        return subprocess.run(["bash", str(self.script)], env=self.env, capture_output=True, text=True, timeout=15)

    def test_prepared_browser_survives_config_isolation(self):
        result = self.run_sidecar()
        self.assertEqual(result.returncode, 0, result.stderr)
        actual = json.loads(result.stdout)
        self.assertEqual(actual, {"browser": str(self.browser), "config": str(self.state / "config")})

    def test_explicit_browser_takes_precedence(self):
        override = self.make_browser("explicit browser")
        self.env["LYCAON_BROWSER_BIN"] = str(override)
        result = self.run_sidecar()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["browser"], str(override))

    def test_missing_resources_prevent_sidecar_start(self):
        (self.browser.parent / "icudtl.dat").unlink()
        result = self.run_sidecar()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")

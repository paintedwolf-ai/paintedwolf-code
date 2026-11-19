"""A signed macOS bundle missing its Developer ID stops before anything builds."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[4]


class BundleSigningTests(unittest.TestCase):
    def test_a_release_without_its_signing_identity_is_refused_before_any_build_tool_runs(self):
        tools = Path(self.enterContext(tempfile.TemporaryDirectory()))
        ran = tools / "ran"
        (tools / "rustc").write_text("#!/bin/sh\necho aarch64-apple-darwin\n")
        for name in ("bun", "go", "cargo"):
            (tools / name).write_text(f"#!/bin/sh\necho {name} >> '{ran}'\nexit 1\n")
        for tool in tools.iterdir():
            tool.chmod(0o755)
        result = subprocess.run(["bash", str(ROOT / "scripts/den-build-bundle.sh")], cwd=ROOT,
                                env={**os.environ, "APPLE_SIGNING_IDENTITY": "", "APPLE_ENGINE_PROVISIONING_PROFILE": "",
                                     "PATH": str(tools) + os.pathsep + os.environ["PATH"]},
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 2)
        self.assertFalse(ran.exists())

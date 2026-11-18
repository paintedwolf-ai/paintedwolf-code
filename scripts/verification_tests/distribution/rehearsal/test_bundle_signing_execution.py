"""Each macOS signing mode is chosen explicitly and decided before any build; none is reached by omission."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[4]
IDENTITY = "Developer ID Application: Example (TEAM123456)"
PROFILE = "engine.provisionprofile"


def decide(host, unsigned, phase="all", **environment):
    """The signing decision den:bundle makes: its status, mode, identity and engine host arguments."""
    script = ('source scripts/toolchain/bundle-signing.sh; bundle_signing "$1" "$2" "$3"; status=$?; '
              'printf "%s\\n%s\\n%s\\n%s\\n" "$status" "$SIGNING_MODE" "$SIGNING_IDENTITY" "${HOST_SIGNING[*]}"')
    signing = {"APPLE_SIGNING_IDENTITY": "", "APPLE_ENGINE_PROVISIONING_PROFILE": "", **environment}
    output = subprocess.run(["bash", "-c", script, "decide", host, unsigned, phase], cwd=ROOT, env={**os.environ, **signing},
                            capture_output=True, text=True, check=True, timeout=30).stdout.split("\n")
    return int(output[0]), output[1], output[2], output[3]


class BundleSigningTests(unittest.TestCase):
    def test_a_release_and_its_package_phase_sign_with_the_developer_id_and_a_profiled_host(self):
        for phase in ("all", "package"):
            with self.subTest(phase=phase):
                self.assertEqual(decide("darwin", "0", phase, APPLE_SIGNING_IDENTITY=IDENTITY, APPLE_ENGINE_PROVISIONING_PROFILE=PROFILE),
                                 (0, "developer-id", IDENTITY, "--release"))

    def test_a_release_missing_any_part_of_its_developer_id_is_refused(self):
        for phase in ("all", "package"):
            for environment in ({}, {"APPLE_SIGNING_IDENTITY": IDENTITY}, {"APPLE_ENGINE_PROVISIONING_PROFILE": PROFILE},
                                {"APPLE_SIGNING_IDENTITY": "-", "APPLE_ENGINE_PROVISIONING_PROFILE": PROFILE}):
                with self.subTest(phase=phase, environment=environment):
                    self.assertEqual(decide("darwin", "0", phase, **environment)[0], 1)

    def test_the_compile_phase_is_unsigned_by_design_and_holds_no_identity(self):
        self.assertEqual(decide("darwin", "0", "compile"), (0, "compile", "", ""))
        self.assertEqual(decide("darwin", "0", "compile", APPLE_SIGNING_IDENTITY=IDENTITY, APPLE_ENGINE_PROVISIONING_PROFILE=PROFILE)[0], 1)
        self.assertEqual(decide("darwin", "1", "compile")[0], 1)

    def test_ad_hoc_signing_is_only_an_explicit_unsigned_macos_bundle(self):
        self.assertEqual(decide("darwin", "1"), (0, "ad-hoc", "-", ""))
        self.assertEqual(decide("darwin", "1", APPLE_SIGNING_IDENTITY=IDENTITY)[0], 1)
        self.assertEqual(decide("darwin", "1", "package")[0], 1)
        self.assertEqual(decide("linux", "1")[0], 1)
        self.assertEqual(decide("linux", "0"), (0, "none", "", ""))

    def test_the_bundle_build_refuses_before_any_build_tool_runs(self):
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
                                capture_output=True, text=True, timeout=120)
        self.assertEqual(result.returncode, 2)
        self.assertFalse(ran.exists())

"""Publication is downstream of a credential-free desktop upgrade gate."""
from pathlib import Path
import os
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]


class WorkflowTests(unittest.TestCase):
    def dry_run(self, *arguments):
        with tempfile.TemporaryDirectory() as temporary:
            tools = Path(temporary)
            for name in ("bun", "minisign"):
                executable = tools / name
                executable.write_text("#!/bin/sh\nexit 97\n")
                executable.chmod(0o755)
            environment = dict(os.environ, PATH=str(tools) + os.pathsep + os.environ["PATH"])
            return subprocess.run(["bash", str(ROOT / "task"), "--dry", "upgrade:rehearse", "--", *arguments],
                                  cwd=ROOT, env=environment, capture_output=True, text=True, timeout=30)

    def test_existing_task_dispatches_bundle_mode_without_building_a_replacement(self):
        result = self.dry_run("--bundle", "--fixtures", "/fixtures", "--evidence", "/evidence", "--case", "1.0.2-preview")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("bundle-rehearse --fixtures /fixtures --evidence /evidence", " ".join((result.stdout + result.stderr).split()))
        self.assertNotIn("build:lycaon-dev", result.stdout + result.stderr)

    def test_preparation_uses_its_own_managed_mode_without_building(self):
        result = self.dry_run("--prepare-bundle", "--manifest", "/manifest", "--archives", "/archives", "--source", "/source", "--output", "/output")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("bundle-prepare --manifest /manifest", " ".join((result.stdout + result.stderr).split()))
        self.assertNotIn("build:lycaon-dev", result.stdout + result.stderr)

    def test_bundle_commands_reach_their_argument_parsers(self):
        for command in ("bundle-prepare", "bundle-rehearse", "bundle-serve"):
            result = subprocess.run([sys.executable, "scripts/release_control.py", command, "--help"], cwd=ROOT,
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("usage:", result.stdout)

    def test_signing_and_native_execution_have_separate_jobs_and_publication_depends_on_them(self):
        workflow = (ROOT / ".github/workflows/release.yml").read_text()
        prepare = workflow.split("  prepare-bundle-upgrade:\n", 1)[1].split("  bundle-upgrade:\n", 1)[0]
        execution = workflow.split("  bundle-upgrade:\n", 1)[1].split("  publish-immutable:\n", 1)[0]
        publication = workflow.split("  publish-immutable:\n", 1)[1].split("    steps:", 1)[0]
        self.assertIn("needs: [classify, ship-gates, aggregate-release, publication-credentials]", prepare)
        self.assertIn("fixture_name: bundle-upgrade-fixtures-${{ github.run_attempt }}", prepare)
        self.assertIn("name: ${{ needs.prepare-bundle-upgrade.outputs.fixture_name }}", execution)
        self.assertIn("environment: release-publication", prepare)
        self.assertIn("FEED_SIGNING_KEYS_JSON:", prepare)
        self.assertIn("./task upgrade:rehearse -- --prepare-bundle", prepare)
        self.assertIn("bundle-upgrade-preparation-evidence", prepare)
        self.assertIn("if: ${{ failure() || cancelled() }}", prepare)
        self.assertNotIn("TAURI_SIGNING_PRIVATE_KEY", prepare)
        self.assertNotIn("secrets.", execution)
        self.assertIn("needs: [classify, prepare-bundle-upgrade]", execution)
        self.assertIn("working-directory: lycaon-den", execution)
        self.assertIn("bun install --frozen-lockfile", execution)
        self.assertIn("max-parallel: 1", execution)
        self.assertIn("matrix: ${{ fromJSON(needs.prepare-bundle-upgrade.outputs.matrix) }}", execution)
        self.assertIn('matrix.case_id', execution)
        self.assertIn("aggregate-release, bundle-upgrade]", publication)
        self.assertIn("ref: ${{ github.workflow_sha }}", prepare)
        self.assertIn("if: always()", execution)

    def test_nightly_installs_the_same_rehearsal_dependencies_before_execution(self):
        workflow = (ROOT / ".github/workflows/nightly.yml").read_text()
        upgrade = workflow.split("  upgrade-path:\n", 1)[1].split("  quarantine:\n", 1)[0]
        self.assertLess(upgrade.index("uses: ./.github/actions/setup-verification"), upgrade.index("run: ./task upgrade:rehearse"))
        self.assertLess(upgrade.index("brew install minisign"), upgrade.index("run: ./task upgrade:rehearse"))
        self.assertNotIn("run: ./task build:lycaon-dev", upgrade)

    def test_release_rehearsal_builds_its_candidate_only_inside_the_managed_target(self):
        workflow = (ROOT / ".github/workflows/release.yml").read_text()
        upgrade = workflow.split("  validate-source:\n", 1)[1].split("  ship-gates:\n", 1)[0]
        self.assertIn("run: ./task upgrade:rehearse", upgrade)
        self.assertNotIn("run: ./task build:lycaon-dev", upgrade)

    def test_source_validation_reuses_setup_and_retains_each_gate_evidence(self):
        workflow = (ROOT / ".github/workflows/release.yml").read_text()
        validation = workflow.split("  validate-source:\n", 1)[1].split("  ship-gates:\n", 1)[0]
        self.assertEqual(validation.count("uses: actions/checkout@"), 1)
        self.assertEqual(validation.count("uses: ./.github/actions/setup-verification"), 1)
        self.assertLess(validation.index("./task release:preflight"), validation.index("./task upgrade:rehearse"))
        self.assertIn("name: release-preflight-${{ github.run_attempt }}", validation)
        self.assertIn("name: release-upgrade-${{ github.run_attempt }}", validation)
        self.assertNotIn("continue-on-error", validation)

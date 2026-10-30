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
        execution = workflow.split("  bundle-upgrade:\n", 1)[1].split("  publish-artifacts:\n", 1)[0]
        publication = workflow.split("  publish-artifacts:\n", 1)[1].split("    steps:", 1)[0]
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


class ReplayTests(unittest.TestCase):
    def test_replay_is_read_only_and_cannot_reach_publication(self):
        workflow = (ROOT / ".github/workflows/release.yml").read_text()
        replay = workflow.split("  replay-bundle-upgrade:\n", 1)[1].split("  classify:\n", 1)[0]
        self.assertIn("inputs.rehearsal_run_id != ''", replay)
        self.assertIn("if: ${{ inputs.rehearsal_run_id == '' }}", workflow)
        self.assertIn("needs: [ship-gates, bundle-upgrade, publish-github-release]", replay)
        self.assertIn("!cancelled()", replay)
        self.assertIn("actions: read", replay)
        self.assertNotIn("secrets.", replay)
        self.assertNotIn("environment: release-publication", replay)
        self.assertIn("artifact-ids: ${{ steps.fixtures.outputs.artifact_id }}", replay)
        self.assertIn('--case "${REHEARSAL_CASE}"', replay)

    def test_fixture_admission_requires_preparation_and_exact_attempt(self):
        from unittest.mock import patch
        from release_upgrade import replay
        run = {"path": ".github/workflows/release.yml", "status": "completed", "event": "push",
               "head_repository": {"full_name": "owner/repo"}, "head_branch": "v1.0.3-rc1", "run_attempt": 2, "head_sha": "a" * 40}
        def rows(path, key):
            if key == "jobs":
                self.assertIn("/attempts/2/jobs", path)
                return [{"name": "prepare-bundle-upgrade", "conclusion": "success"}]
            return [{"name": "bundle-upgrade-fixtures-1", "expired": False, "id": 1},
                    {"name": "bundle-upgrade-fixtures-2", "expired": False, "id": 2}]
        with patch.dict(os.environ, GITHUB_REPOSITORY="owner/repo"), patch.object(replay, "api", return_value=run), patch.object(replay, "pages", side_effect=rows):
            self.assertEqual(replay.select("123", "")["artifact_id"], 2)
            for field, value in (("status", "in_progress"), ("path", "ci.yml"), ("head_branch", "unmerged-feature"), ("head_repository", {"full_name": "foreign/repo"})):
                with patch.dict(run, {field: value}), self.assertRaises(ValueError):
                    replay.select("123", "")
            with patch.object(replay, "pages", return_value=[]), self.assertRaises(ValueError):
                replay.select("123", "")
            with self.assertRaises(ValueError):
                replay.select("123", "v1.0.3-rc1")
            with self.assertRaises(ValueError):
                replay.select("../123", "")

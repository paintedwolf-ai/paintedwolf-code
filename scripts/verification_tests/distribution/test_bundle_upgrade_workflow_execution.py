"""Publication is downstream of a credential-free desktop upgrade gate."""
from pathlib import Path
import subprocess
import sys
import unittest

ROOT = Path(__file__).resolve().parents[3]


class WorkflowTests(unittest.TestCase):
    def test_existing_task_dispatches_bundle_mode_without_building_a_replacement(self):
        result = subprocess.run([str(ROOT / "task"), "--dry", "upgrade:rehearse", "--", "--bundle", "--fixtures", "/fixtures", "--evidence", "/evidence"],
                                cwd=ROOT, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("bundle-rehearse --fixtures /fixtures --evidence /evidence", " ".join((result.stdout + result.stderr).split()))
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
        self.assertIn("environment: release-publication", prepare)
        self.assertIn("FEED_SIGNING_KEYS_JSON:", prepare)
        self.assertNotIn("TAURI_SIGNING_PRIVATE_KEY", prepare)
        self.assertNotIn("secrets.", execution)
        self.assertIn("needs: [classify, prepare-bundle-upgrade]", execution)
        self.assertIn("aggregate-release, bundle-upgrade]", publication)
        self.assertIn("ref: ${{ github.workflow_sha }}", prepare)
        self.assertIn("if: always()", execution)

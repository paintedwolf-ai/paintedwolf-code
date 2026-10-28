"""Setup packages pass native admission before the ordinary immutable publication boundary."""

from pathlib import Path
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[3]


class RepositorySetupWorkflowTests(unittest.TestCase):
    def test_admitted_setup_assets_follow_shared_release_gates_and_mirror(self):
        jobs = yaml.safe_load((ROOT / ".github/workflows/release.yml").read_text())["jobs"]
        self.assertNotIn("linux-candidates", jobs)
        native = jobs["linux-repositories"]
        self.assertEqual(native["environment"], "release-signing")
        build = next(step for step in native["steps"] if step.get("name") == "Build and admit repository setup packages")
        self.assertEqual(build["env"]["LINUX_PACKAGE_SIGNING_KEY"], "${{ secrets.LINUX_PACKAGE_SIGNING_KEY }}")
        self.assertIn("scripts.linux_release.release_repository_setup", build["run"])
        upload = next(step for step in native["steps"] if step.get("with", {}).get("name") == "linux-repository-setup")
        self.assertIn("linux-setup/*", upload["with"]["path"])
        def ancestors(name):
            needs = jobs[name].get("needs", [])
            if isinstance(needs, str):
                needs = [needs]
            return set(needs).union(*(ancestors(parent) for parent in needs))
        self.assertIn("linux-repositories", ancestors("publication-credentials"))
        for name in ("publish-immutable", "publish-github-release"):
            with self.subTest(job=name):
                self.assertIn("publication-credentials", ancestors(name))
                steps = jobs[name]["steps"]
                download = next(step for step in steps if step.get("with", {}).get("name") == "linux-repository-setup")
                self.assertEqual(download["with"]["path"], "release-artifacts")
                self.assertTrue(any("for artifact in release-artifacts/*; do" in step.get("run", "") for step in steps))
        mirror = jobs["publish-github-release"]["steps"][-1]["run"]
        self.assertIn('gh release create "v${VERSION}" release-artifacts/*', mirror)

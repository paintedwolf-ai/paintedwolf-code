"""The release signs and publishes what preparation built: every build precedes every public side effect."""

from pathlib import Path
import re
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[3]
WORKFLOWS = ROOT / ".github/workflows"
PREPARE = "./.github/workflows/release-prepare.yml"
DELIVERY = "./.github/workflows/release-linux-delivery.yml"
# Commands that compile the application or a closure from source.
COMPILE = re.compile(r"den:bundle(?! -- --phase package)|nix build|cargo build|tauri build|go build")
# Commands that change what users can download or are offered.
PUBLIC = re.compile(r"release-r2-immutable-put|release-r2-publish-pointer|release_repositories publish"
                    r"|nix_cache\.release_lane publish|gh release create|git push|/dispatches")


def load(name: str) -> dict:
    return yaml.load((WORKFLOWS / name).read_text(), Loader=yaml.BaseLoader)["jobs"]


def needs(job: dict) -> list[str]:
    value = job.get("needs", [])
    return [value] if isinstance(value, str) else value


def ancestors(jobs: dict, name: str) -> set[str]:
    seen: set[str] = set()
    pending = list(needs(jobs[name]))
    while pending:
        current = pending.pop()
        if current not in seen:
            seen.add(current)
            pending.extend(needs(jobs[current]))
    return seen


def runs(job: dict) -> str:
    return "\n".join(step.get("run", "") for step in job.get("steps", []))


def linux_steps(job: dict) -> str:
    return "\n".join(step.get("run", "") for step in job.get("steps", [])
                     if "matrix.goos == 'darwin'" not in step.get("if", ""))


class ReleaseGraphTests(unittest.TestCase):
    def setUp(self):
        self.jobs = load("release.yml")
        self.builds = {name for name, job in self.jobs.items()
                       if job.get("uses") == PREPARE or name in {"build-release", "import-nix-closures"}}
        self.upgrades = {"bundle-upgrade"}
        delivery = load("release-linux-delivery.yml")
        self.publishing = {name for name, job in self.jobs.items()
                           if PUBLIC.search(runs(job)) or job.get("uses") == DELIVERY
                           or (job.get("permissions") or {}).get("contents") == "write"}
        self.assertTrue(all(PUBLIC.search(runs(job)) for job in delivery.values()))

    def test_every_publishing_job_needs_every_build_import_and_upgrade(self):
        self.assertGreaterEqual(len(self.publishing), 7)
        self.assertEqual(self.builds, {"prepare", "build-release", "import-nix-closures"})
        for name in sorted(self.publishing):
            with self.subTest(name):
                self.assertLessEqual(self.builds | self.upgrades, ancestors(self.jobs, name))

    def test_no_build_import_or_upgrade_needs_a_publishing_job(self):
        for name in sorted(self.builds | self.upgrades):
            with self.subTest(name):
                self.assertFalse(ancestors(self.jobs, name) & self.publishing)

    def test_no_release_matrix_runs_one_job_at_a_time(self):
        for workflow in ("release.yml", "release-prepare.yml", "release-linux-delivery.yml"):
            for name, job in load(workflow).items():
                with self.subTest(f"{workflow}:{name}"):
                    self.assertNotEqual((job.get("strategy") or {}).get("max-parallel"), "1")

    def test_linux_and_nix_legs_compile_nothing(self):
        self.assertIsNone(COMPILE.search(linux_steps(self.jobs["build-release"])))
        self.assertIsNone(COMPILE.search(runs(self.jobs["import-nix-closures"])))
        for name, job in load("release-linux-delivery.yml").items():
            with self.subTest(name):
                self.assertIsNone(COMPILE.search(runs(job)))

    def test_the_signing_job_packages_the_prepared_application_without_compiling(self):
        build = self.jobs["build-release"]
        self.assertIn("prepare", needs(build))
        self.assertIsNone(COMPILE.search(runs(build)))
        self.assertIn("den:bundle -- --phase package", runs(build))
        verify = runs(build).index("release_preparation verify")
        self.assertLess(verify, runs(build).index("--phase package"))
        self.assertLess(verify, runs(build).index("release_signing"))


if __name__ == "__main__":
    unittest.main()

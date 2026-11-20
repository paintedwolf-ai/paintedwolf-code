"""A release proves its inputs, credentials and environments before building, and publishes after its upgrade gate."""
from pathlib import Path
import os
import re
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[3]


PROBE = "release-secrets-check.yml"


def load(name):
    return yaml.safe_load((ROOT / ".github/workflows" / name).read_text())


def needs(job):
    value = job.get("needs", [])
    return [value] if isinstance(value, str) else value


def ancestors(jobs, name):
    seen = set()
    pending = list(needs(jobs[name]))
    while pending:
        job = pending.pop()
        if job not in seen:
            seen.add(job)
            pending.extend(needs(jobs[job]))
    return seen


def secrets(value):
    return set(re.findall(r"secrets\.([A-Z][A-Z0-9_]*)", yaml.safe_dump(value)))


def group(workflow, event_name, run_id=1, **inputs):
    """Evaluate a concurrency group; GitHub's && and || select operands as Python's and and or do."""
    expression = workflow["concurrency"]["group"]
    if not expression.startswith("${{"):
        return expression
    body = expression.removeprefix("${{").removesuffix("}}").replace("&&", " and ").replace("||", " or ")
    scope = {"github": SimpleNamespace(event_name=event_name, run_id=run_id),
             "inputs": SimpleNamespace(**{"release_tag": "", "rehearsal_run_id": "", **inputs}),
             "format": lambda template, *values: template.format(*values)}
    return eval(body, {"__builtins__": {}}, scope)


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


class ReplayTests(unittest.TestCase):
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


class ReleaseOrderTests(unittest.TestCase):
    def setUp(self):
        self.release = load("release.yml")
        self.jobs = self.release["jobs"]
        self.probe = {name: job for name, job in load(PROBE)["jobs"].items() if name != "feed-credentials"}

    def test_static_metadata_is_checked_before_the_release_is_classified(self):
        steps = self.jobs["classify"]["steps"]
        static = [index for index, step in enumerate(steps) if "release:preflight -- --static" in step.get("run", "")]
        classified = [index for index, step in enumerate(steps) if step.get("id") == "release"]
        self.assertEqual(len(static), 1)
        self.assertLess(static[0], classified[0])
        self.assertNotIn("if", steps[static[0]])

    def test_every_job_after_classification_waits_for_the_probe(self):
        probe = self.jobs["credentials"]
        self.assertEqual(probe["uses"], "./.github/workflows/" + PROBE)
        self.assertEqual(needs(probe), ["classify"])
        self.assertNotIn("if", probe)
        later = set(self.jobs) - {"classify", "credentials", "replay-bundle-upgrade"}
        self.assertEqual({name for name in later if "credentials" not in ancestors(self.jobs, name)}, set())

    def test_the_probe_enters_every_environment_and_reads_every_credential_of_the_release(self):
        delivery = load("release-linux-delivery.yml")["jobs"]
        released = {**self.jobs, **delivery}
        environments = {job["environment"] for job in released.values() if "environment" in job}
        self.assertEqual({job["environment"] for job in self.probe.values()}, environments)
        self.assertEqual(secrets(released) - secrets(self.probe), set())

    def test_the_probe_runs_whenever_no_maintenance_is_chosen(self):
        self.assertEqual(self.jobs["credentials"].get("with", {}).keys(), {"source_sha"})
        for name, job in self.probe.items():
            with self.subTest(job=name):
                self.assertEqual(needs(job), ["feed-credentials"])
                self.assertEqual(job["if"], "${{ !cancelled() && needs.feed-credentials.result == 'skipped' }}")

    def test_a_called_probe_and_the_storage_rehearsal_wait_in_no_shared_group(self):
        self.assertNotIn("concurrency", load(PROBE))
        rehearsal = load("release-system-live-test.yml")["concurrency"]["group"]
        others = {path.name: yaml.safe_load(path.read_text()).get("concurrency", {}).get("group")
                  for path in (ROOT / ".github/workflows").glob("*.yml") if path.name != "release-system-live-test.yml"}
        self.assertNotIn(rehearsal, others.values())

    def test_only_a_publishing_run_takes_the_turn_a_halt_waits_for(self):
        halt = group(load("release-halt.yml"), "workflow_dispatch")
        self.assertEqual(group(self.release, "push"), halt)
        self.assertEqual(group(self.release, "workflow_dispatch", release_tag="v1.2.3"), halt)
        rehearsals = [group(self.release, "workflow_dispatch", run_id=7),
                      group(self.release, "workflow_dispatch", run_id=8),
                      group(self.release, "workflow_dispatch", run_id=9, rehearsal_run_id="41"),
                      group(self.release, "workflow_dispatch", run_id=10, rehearsal_run_id="41", release_tag="v1.2.3")]
        self.assertNotIn(halt, rehearsals)
        self.assertEqual(len(set(rehearsals)), len(rehearsals))
        self.assertIs(self.release["concurrency"]["cancel-in-progress"], False)

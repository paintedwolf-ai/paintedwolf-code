"""A release proves its metadata, credentials and environments before it spends a build."""
import json
from pathlib import Path
import re
from types import SimpleNamespace
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[3]
WORKFLOWS = ROOT / ".github/workflows"
PROBE = "release-secrets-check.yml"


def load(name):
    return yaml.safe_load((WORKFLOWS / name).read_text())


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
        catalog = json.loads((ROOT / "packaging/release-platforms.json").read_text())["platforms"]
        published = {row["goos"] for row in catalog if row["publication"] == "public"}
        unprobed = secrets(released) - secrets(self.probe)
        # Signing credentials of a platform the catalog does not publish are never read.
        self.assertEqual(unprobed, set() if "windows" in published else {
            "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET", "AZURE_TENANT_ID"})

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
                  for path in WORKFLOWS.glob("*.yml") if path.name != "release-system-live-test.yml"}
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

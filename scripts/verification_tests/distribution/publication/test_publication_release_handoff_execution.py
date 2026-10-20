"""A current workflow handoff attests producer jobs, never workflow completion."""
import copy
import unittest
from scripts.linux_release.publication.release_handoff import attest, AGGREGATE_STEPS, CONSUMERS


class CurrentProducerHandoffTests(unittest.TestCase):
    def setUp(self):
        self.producer = dict(repository_id=7, run_id=12, run_attempt=2, controller_commit="a" * 40)
        self.run = dict(id=12, run_attempt=2, head_sha="a" * 40, head_branch="main",
                        path=".github/workflows/release.yml", event="workflow_dispatch",
                        status="in_progress", conclusion=None,
                        repository=dict(id=7, full_name="owner/repo"), head_repository=dict(id=7))
        self.rows = [self.job(1, "aggregate-release", AGGREGATE_STEPS, False),
                     self.job(2, "publish-immutable", {CONSUMERS["publish-immutable"]}, True)]
        self.endpoints = []

    def job(self, identifier, name, names, active):
        status, conclusion = ("in_progress", None) if active else ("completed", "success")
        return dict(id=identifier, name=name, run_id=12, head_sha="a" * 40, head_branch="main",
                    status=status, conclusion=conclusion,
                    steps=[dict(number=index + 1, name=name, status=status, conclusion=conclusion)
                           for index, name in enumerate(sorted(names))])

    def api(self, endpoint):
        self.endpoints.append(endpoint)
        return self.run if endpoint.endswith("/runs/12") else {"jobs": self.rows}

    def selected(self):
        return attest(self.api, "owner/repo", self.producer,
                      {"aggregate-release": AGGREGATE_STEPS}, "publish-immutable")

    def test_attempt_specific_jobs_without_repeated_attempt_field(self):
        result = self.selected()
        self.assertTrue(result[0]["completed"])
        self.assertFalse(result[1]["completed"])
        self.assertIn("/attempts/2/jobs?", self.endpoints[1])
        self.assertNotIn("run_attempt", self.rows[0])

    def test_run_identity_and_active_controller_are_mandatory(self):
        for field, value in (("run_attempt", 1), ("head_sha", "b" * 40),
                             ("head_branch", "feature"), ("event", "pull_request"),
                             ("status", "completed"), ("conclusion", "success"),
                             ("repository", dict(id=8, full_name="owner/repo"))):
            original = copy.deepcopy(self.run)
            self.run[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.selected()
            self.run = original

    def test_failed_skipped_missing_or_duplicate_producer_is_not_completion(self):
        original = copy.deepcopy(self.rows)
        for mutate in (lambda: self.rows[0].update(conclusion="failure"),
                       lambda: self.rows[0]["steps"][0].update(conclusion="skipped"),
                       lambda: self.rows[0]["steps"].pop(),
                       lambda: self.rows.append(copy.deepcopy(self.rows[0])),
                       lambda: self.rows[0].update(run_id=13)):
            mutate()
            with self.assertRaises(ValueError):
                self.selected()
            self.rows = copy.deepcopy(original)

    def test_consumer_must_be_executing_the_fixed_step(self):
        self.rows[1]["steps"][0]["status"] = "completed"
        with self.assertRaises(ValueError):
            self.selected()
        with self.assertRaises(ValueError):
            attest(self.api, "owner/repo", self.producer, {}, "arbitrary-job")

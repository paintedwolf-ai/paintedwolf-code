"""Campaign selection uses server workflow facts and exact artifact/attempt identities."""

import copy
import unittest

from linux_release.publication_provenance import select, select_qualification, verify_producer


class PublicationProvenanceTests(unittest.TestCase):
    def fixture(self):
        run = {"id": 123, "repository": {"id": 42, "full_name": "owner/repo"},
               "head_repository": {"id": 42}, "head_sha": "a" * 40, "run_attempt": 2,
               "path": ".github/workflows/release.yml", "head_branch": "main",
               "event": "workflow_dispatch", "status": "completed", "conclusion": "success"}
        artifact = {"id": 456, "name": "linux-final-admission-inputs", "expired": False,
                    "size_in_bytes": 100, "digest": "sha256:" + "b" * 64,
                    "workflow_run": {"id": 123, "repository_id": 42,
                                     "head_repository_id": 42, "head_sha": "a" * 40}}
        return run, artifact

    def selection(self, run, rows):
        calls = []
        def api(path):
            calls.append(path)
            return run if path.endswith("/runs/123") else {"artifacts": rows}
        return select(api, "owner/repo", 42, 123), calls

    def test_selects_exact_server_artifact_then_binds_download_to_source_and_attempt(self):
        run, artifact = self.fixture()
        selected, calls = self.selection(run, [artifact])
        self.assertEqual(selected["artifact_id"], 456)
        self.assertEqual(selected["artifact_sha256"], "b" * 64)
        self.assertEqual(len(calls), 2)
        producer = {"format_version": 1, "repository_id": 42, "run_id": 123, "run_attempt": 2,
                    "controller_commit": "a" * 40, "source_commit": "c" * 40, "source_tree": "d" * 40}
        verify_producer(selected, producer, "c" * 40, "d" * 40)
        for field, value in [("run_attempt", 1), ("source_commit", "e" * 40),
                             ("source_tree", "e" * 40), ("controller_commit", "e" * 40),
                             ("format_version", True)]:
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, "producer or source"):
                verify_producer(selected, {**producer, field: value}, "c" * 40, "d" * 40)

    def test_rejects_failed_fork_branch_event_and_workflow_before_artifact_selection(self):
        run, artifact = self.fixture()
        for field, value in [("conclusion", "failure"), ("status", "in_progress"),
                             ("event", "pull_request"), ("head_branch", "feature"),
                             ("path", ".github/workflows/linux-candidates.yml"),
                             ("repository", {"id": 41, "full_name": "owner/repo"}),
                             ("head_repository", {"id": 41}), ("run_attempt", True)]:
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, "protected main"):
                self.selection({**run, field: value}, [artifact])

    def test_rejects_missing_ambiguous_expired_and_mismatched_artifacts(self):
        run, artifact = self.fixture()
        for rows in [[], [artifact, artifact], [{**artifact, "expired": True}],
                     [{**artifact, "digest": None}], [{**artifact, "size_in_bytes": True}],
                     [{**artifact, "workflow_run": {**artifact["workflow_run"], "head_sha": "e" * 40}}],
                     [{**artifact, "workflow_run": {**artifact["workflow_run"], "repository_id": 41}}]]:
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                self.selection(run, rows)

    def test_paginates_and_refuses_duplicate_name_on_later_page(self):
        run, artifact = self.fixture()
        first = [{"name": "unrelated-" + str(index)} for index in range(99)] + [artifact]
        def api(path):
            if path.endswith("/runs/123"):
                return run
            return {"artifacts": first if path.endswith("page=1") else [copy.deepcopy(artifact)]}
        with self.assertRaisesRegex(ValueError, "exactly one"):
            select(api, "owner/repo", 42, 123)

    def test_raw_qualification_and_retained_campaign_are_independent_artifact_roles(self):
        run, final = self.fixture()
        raw = {**final, "id": 789, "name": "linux-installed-raw-campaign"}
        def api(path):
            return run if path.endswith("/runs/123") else {"artifacts": [final, raw]}
        self.assertEqual(select(api, "owner/repo", 42, 123)["artifact_id"], 456)
        self.assertEqual(select_qualification(api, "owner/repo", 42, 123)["artifact_id"], 789)
        def missing(path):
            return run if path.endswith("/runs/123") else {"artifacts": [final]}
        with self.assertRaises(ValueError):
            select_qualification(missing, "owner/repo", 42, 123)

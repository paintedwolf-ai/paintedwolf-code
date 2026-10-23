"""Only complete exact-source merge executions supply reusable lane evidence."""
import copy
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import zipfile

from ci_policy import lane_proof, proof_reuse
from ci_policy.hosted_plan import matrix


class LaneProofTests(unittest.TestCase):
    def setUp(self):
        self.environment = {"GITHUB_EVENT_NAME": "merge_group", "GITHUB_REPOSITORY": "org/repo",
                            "GITHUB_RUN_ID": "42", "GITHUB_RUN_ATTEMPT": "2", "GITHUB_SHA": "a" * 40}
        self.record = {"lane": "behavior", "shard": "1/2", "status": "passed", "started_at": 10, "finished_at": 50}
        self.run = {"id": 42, "run_attempt": 2, "head_sha": "a" * 40}
        self.tree = "b" * 40
        self.rows = matrix("check")

    def proof(self, lane="behavior", shard="1/2"):
        record = {**self.record, "lane": lane, "shard": shard}
        with patch.object(lane_proof.subprocess, "check_output", side_effect=["a" * 40, self.tree]), \
                patch.object(lane_proof.subprocess, "run"):
            return lane_proof.seal(Path("/source"), record, self.environment)

    def test_seal_requires_complete_successful_merge_execution(self):
        self.assertEqual(self.proof()["source_tree"], self.tree)
        for changes in ({"status": "failed"}, {"lane": "limits"}, {"packages": ["./internal/foo"]}, {"execution_error": "failure"}):
            self.assertIsNone(lane_proof.seal(Path("/source"), {**self.record, **changes}, self.environment))
        self.assertIsNone(lane_proof.seal(Path("/source"), self.record, {**self.environment, "GITHUB_EVENT_NAME": "pull_request"}))
        with patch.object(lane_proof.subprocess, "check_output", return_value="c" * 40), self.assertRaises(ValueError):
            lane_proof.seal(Path("/source"), self.record, self.environment)
        with patch.object(lane_proof.subprocess, "check_output", return_value="a" * 40), \
                patch.object(lane_proof.subprocess, "run", side_effect=subprocess.CalledProcessError(1, "git")), \
                self.assertRaises(subprocess.CalledProcessError):
            lane_proof.seal(Path("/source"), self.record, self.environment)

    def test_every_shard_must_be_proven_once_by_one_attempt(self):
        first, second = self.proof(), self.proof(shard="2/2")
        for proofs in ([first], [first, first, second], [first, {**second, "run_attempt": 1}]):
            remaining, reused = proof_reuse.select(self.rows, proofs, self.run, "org/repo", self.tree)
            self.assertEqual(remaining, self.rows)
            self.assertEqual(reused, [])
        remaining, reused = proof_reuse.select(self.rows, [first, second], self.run, "org/repo", self.tree)
        self.assertEqual(len(reused), 2)
        self.assertFalse(any(row["lane"] == "behavior" for row in remaining["include"]))
        self.assertTrue(any(row["lane"] == "limits" for row in remaining["include"]))

    def test_source_recipe_platform_and_provenance_mismatches_never_grant_reuse(self):
        proof = self.proof("lint", "")
        row = next(row for row in self.rows["include"] if row["lane"] == "lint")
        for field, value in [("version", True), ("repository", "other/repo"), ("source_tree", "changed"),
                             ("source_sha", "changed"), ("recipe", "changed"), ("runner", "macos-15"),
                             ("status", "failed"), ("scope", "affected"), ("run_id", 43), ("shard", "1/2")]:
            with self.subTest(field=field):
                self.assertFalse(proof_reuse.matches({**proof, field: value}, row, self.run, "org/repo", self.tree))
        self.assertFalse(proof_reuse.matches({**proof, "extra": True}, row, self.run, "org/repo", self.tree))

    def test_archive_limits_and_member_identity(self):
        def archive(name, value):
            stream = io.BytesIO()
            with zipfile.ZipFile(stream, "w") as output:
                output.writestr(name, value)
            return stream.getvalue()
        proof = self.proof()
        self.assertEqual(proof_reuse.decode(archive("lane-proof.json", json.dumps(proof))), proof)
        for raw in (b"x" * (proof_reuse.LIMIT + 1), archive("../lane-proof.json", "{}"),
                    archive("lane-proof.json", "x" * (proof_reuse.LIMIT + 1))):
            with self.assertRaises(ValueError):
                proof_reuse.decode(raw)

    def test_api_failure_executes_all_unproven_lanes(self):
        with patch.object(proof_reuse, "candidates", side_effect=RuntimeError("unexpected")):
            with self.assertRaises(RuntimeError):
                proof_reuse.resolve(self.rows, "org/repo", "a" * 40, self.tree)
        with patch.object(proof_reuse, "candidates", side_effect=subprocess.CalledProcessError(1, "gh")):
            remaining, audit = proof_reuse.resolve(self.rows, "org/repo", "a" * 40, self.tree)
        self.assertEqual(remaining, self.rows)
        self.assertEqual(audit["reused"], [])
        self.assertEqual(len(audit["lookup_errors"]), 1)

    def test_lookup_requires_trusted_successful_workflow_and_equal_tree(self):
        valid = {**self.run, "event": "merge_group", "conclusion": "success", "path": ".github/workflows/ci.yml",
                 "head_repository": {"full_name": "org/repo"}}
        invalid = [{**valid, "event": "pull_request"}, {**valid, "conclusion": "failure"},
                   {**valid, "path": ".github/workflows/other.yml"}, {**valid, "head_repository": {"full_name": "fork/repo"}}]
        with patch.object(proof_reuse, "api", side_effect=[{"workflow_runs": invalid + [valid]}, {"tree": {"sha": self.tree}}]):
            self.assertEqual(list(proof_reuse.candidates("org/repo", self.tree)), [valid])
        with patch.object(proof_reuse, "api", side_effect=[{"workflow_runs": [valid]}, {"tree": {"sha": "other"}}]):
            self.assertEqual(list(proof_reuse.candidates("org/repo", self.tree)), [])

    def test_resolution_records_reused_and_executed_lanes(self):
        with patch.object(proof_reuse, "candidates", return_value=[self.run]), \
                patch.object(proof_reuse, "read_proofs", return_value=[self.proof("lint", "")]):
            remaining, audit = proof_reuse.resolve(self.rows, "org/repo", "a" * 40, self.tree)
        self.assertEqual([proof["lane"] for proof in audit["reused"]], ["lint"])
        self.assertEqual(len(remaining["include"]), len(self.rows["include"]) - 1)
        self.assertEqual(len(audit["execute"]), len(remaining["include"]))

    def test_publication_writes_only_eligible_proofs(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {**self.environment, "RUNNER_TEMP": directory}):
            with patch.object(lane_proof, "seal", return_value=self.proof()):
                lane_proof.publish(Path("/source"), self.record)
            self.assertEqual(json.loads((Path(directory) / "lane-proof.json").read_text())["run_id"], 42)

    def test_download_ignores_missing_expired_and_oversized_evidence(self):
        entries = [{"id": 1, "name": "other", "size_in_bytes": 1},
                   {"id": 2, "name": "lane-proof-expired", "expired": True, "size_in_bytes": 1},
                   {"id": 3, "name": "lane-proof-large", "size_in_bytes": proof_reuse.LIMIT + 1},
                   {"id": 4, "name": "lane-proof-valid", "size_in_bytes": 1}]
        with patch.object(proof_reuse, "pages", return_value=entries), \
                patch.object(proof_reuse.subprocess, "check_output", return_value=b"zip") as fetch, \
                patch.object(proof_reuse, "decode", return_value={"proof": True}):
            self.assertEqual(proof_reuse.read_proofs("org/repo", self.run), [{"proof": True}])
        self.assertEqual(fetch.call_count, 1)

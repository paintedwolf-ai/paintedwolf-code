import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import verification_verdict as ci
from ci_policy import hosted_plan
import verification_plan as planning
from verification_execute import shard_packages


class VerificationVerdictTests(unittest.TestCase):
    def test_aggregate_rejects_failure_cancellation_skip_and_missing_results(self):
        ci.require_success({"a": {"result": "success"}, "b": {"result": "success"}})
        for status in ["failure", "cancelled", "skipped", "", None]:
            with self.subTest(status=status), self.assertRaises(ValueError):
                ci.require_success({"a": {"result": "success"}, "b": {"result": status}})
        for results in [{}, [], {"a": {}}]:
            with self.assertRaises(ValueError):
                ci.require_success(results)


    def test_aggregate_accepts_only_the_skips_a_tier_declares(self):
        ci.require_success({"verification": {"result": "success"}, "platform": {"result": "skipped"}}, ["platform"])
        for platform, skipped in [("success", ["platform"]), ("failure", ["platform"]), ("skipped", [])]:
            with self.subTest(platform=platform, skipped=skipped), self.assertRaises(ValueError):
                ci.require_success({"verification": {"result": "success"}, "platform": {"result": platform}}, skipped)
        # Declaring a skip never excuses the jobs the tier does run.
        with self.assertRaises(ValueError):
            ci.require_success({"verification": {"result": "skipped"}, "platform": {"result": "skipped"}}, ["platform"])
        with self.assertRaises(ValueError):
            ci.require_success({"verification": {"result": "success"}}, ["platform"])


    def test_draft_pull_requests_never_pass_the_required_check(self):
        # Drafts skip verification, and a skipped required check would otherwise read as passing.
        for results in [{"verification": {"result": "skipped"}, "platform": {"result": "skipped"}},
                        {"verification": {"result": "success"}, "platform": {"result": "skipped"}}]:
            with self.subTest(results=results), self.assertRaisesRegex(ValueError, "ready for review"):
                ci.require_success(results, ["platform"], draft=True)


    def test_release_commit_needs_exact_main_qualification(self):
        good = {"head_sha": "abc", "head_branch": "main", "conclusion": "success", "event": "push"}
        with patch.object(ci, "api", return_value={"workflow_runs": [good]}):
            ci.require_qualification("owner/repo", "abc")
        for changed in ({"head_sha": "other"}, {"head_branch": "feature"}, {"conclusion": "failure"},
                        {"event": "merge_group"}, {"event": "pull_request"}):
            with patch.object(ci, "api", return_value={"workflow_runs": [{**good, **changed}]}), self.assertRaises(ValueError):
                ci.require_qualification("owner/repo", "abc")


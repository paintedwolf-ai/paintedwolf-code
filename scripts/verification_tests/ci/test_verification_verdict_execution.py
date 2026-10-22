import unittest
from unittest.mock import patch

from ci_policy import verification_verdict as ci


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
        from ci_policy import qualification_receipt
        sha = "a" * 40
        good = {"id": 42, "run_attempt": 1, "head_sha": sha, "head_branch": "main", "conclusion": "success", "event": "push"}
        proof = dict(version=1, repository="owner/repo", run_id=42, run_attempt=1, event="push",
                     source_sha=sha, source_tree="c" * 40, controller_sha=sha,
                     checks={name: "success" for name in qualification_receipt.CHECKS})
        with patch.object(ci, "api", return_value={"workflow_runs": [good]}), \
                patch.object(qualification_receipt, "aggregate_passed", return_value=True), \
                patch.object(qualification_receipt, "from_run", return_value=proof):
            self.assertEqual(ci.require_qualification("owner/repo", sha), good)
        for changed in ({}, {"head_sha": "b" * 40}, {"head_branch": "feature"}, {"conclusion": "failure"},
                        {"event": "merge_group"}, {"event": "pull_request"}):
            with patch.object(ci, "api", return_value={"workflow_runs": [{**good, **changed}]}), \
                    patch.object(ci, "pages", return_value=[]), \
                    patch.object(qualification_receipt, "aggregate_passed", return_value=True), \
                    patch.object(qualification_receipt, "from_run", return_value=None), self.assertRaises(ValueError):
                ci.require_qualification("owner/repo", sha)

    def test_successful_run_with_skipped_aggregate_cannot_qualify_a_source(self):
        from ci_policy import qualification_receipt
        sha = "a" * 40
        run = dict(id=42, run_attempt=1, head_sha=sha, head_branch="main", conclusion="success", event="push")
        with patch.object(ci, "api", return_value={"workflow_runs": [run]}), patch.object(ci, "pages", return_value=[]), \
                patch.object(qualification_receipt, "aggregate_passed", return_value=False), \
                patch.object(qualification_receipt, "from_run") as read, self.assertRaises(ValueError):
            ci.require_qualification("owner/repo", sha)
        read.assert_not_called()

    def test_historical_source_uses_a_receipt_bound_to_the_current_controller(self):
        from ci_policy import qualification_receipt
        sha, controller = "a" * 40, "b" * 40
        run = dict(id=42, run_attempt=1, head_sha=controller, head_branch="main", conclusion="success", event="workflow_dispatch",
                   display_title=f"Qualification {sha}")
        proof = dict(version=1, repository="owner/repo", run_id=42, run_attempt=1, event="workflow_dispatch",
                     source_sha=sha, source_tree="c" * 40, controller_sha=controller,
                     checks={name: "success" for name in qualification_receipt.CHECKS})
        for receipt, accepted in [(proof, True), ({**proof, "run_attempt": 2}, False), (None, False)]:
            with self.subTest(receipt=receipt), patch.object(ci, "api", return_value={"workflow_runs": []}), \
                    patch.object(ci, "pages", return_value=[run]), \
                    patch.object(qualification_receipt, "aggregate_passed", return_value=True), \
                    patch.object(qualification_receipt, "from_run", return_value=receipt):
                if accepted:
                    self.assertEqual(ci.require_qualification("owner/repo", sha), run)
                else:
                    with self.assertRaises(ValueError):
                        ci.require_qualification("owner/repo", sha)

    def test_dispatch_lookup_skips_unrelated_candidates_without_downloading_receipts(self):
        from ci_policy import qualification_receipt
        from urllib.parse import parse_qs, urlparse
        sha = "a" * 40
        unrelated = dict(id=7, head_branch="main", conclusion="success", event="workflow_dispatch",
                         display_title="Qualification " + "b" * 40)
        with patch.object(ci, "api", return_value={"workflow_runs": []}) as direct, \
                patch.object(ci, "pages", return_value=[unrelated]) as pages, \
                patch.object(qualification_receipt, "from_run") as read, \
                patch.object(qualification_receipt, "aggregate_passed") as jobs:
            self.assertIsNone(ci.qualified_run("owner/repo", sha))
            read.assert_not_called()
            jobs.assert_not_called()
            for request in (direct.call_args.args[0], pages.call_args.args[0]):
                created = parse_qs(urlparse(request).query)["created"][0]
                self.assertTrue(created.startswith(">="))
                self.assertTrue(created.endswith("Z"))
